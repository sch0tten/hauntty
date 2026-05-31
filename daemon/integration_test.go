package daemon

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sch0tten/hauntty/protocol"
)

// cmdResult collects the responses a single command emits.
type cmdResult struct {
	mu    sync.Mutex
	resps []protocol.Response
	done  chan protocol.Response
}

func (r *cmdResult) all() []protocol.Response {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]protocol.Response, len(r.resps))
	copy(out, r.resps)
	return out
}

// runCmd injects a command and monitors it, returning the terminal done/prompt
// responses observed within the timeout. Each command uses its own in-memory
// connection encoder, mirroring a distinct client connection.
func runCmd(t *testing.T, c *Controller, sess *Session, seq int, cmd string, nonInteractive bool, deadline time.Duration, wait time.Duration) *cmdResult {
	t.Helper()
	srv, cli := net.Pipe()
	enc := protocol.NewEncoder(srv)
	dec := protocol.NewDecoder(cli)

	res := &cmdResult{done: make(chan protocol.Response, 1)}
	go func() {
		for {
			var resp protocol.Response
			if err := dec.Decode(&resp); err != nil {
				return
			}
			res.mu.Lock()
			res.resps = append(res.resps, resp)
			res.mu.Unlock()
			if resp.Op == protocol.OpDone {
				select {
				case res.done <- resp:
				default:
				}
			}
		}
	}()

	if err := sess.Exec(seq, cmd); err != nil {
		srv.Close()
		cli.Close()
		t.Fatalf("exec seq %d: %v", seq, err)
	}
	c.ExecCommand(sess, seq, cmd, enc, nonInteractive, deadline)

	t.Cleanup(func() { srv.Close(); cli.Close() })
	return res
}

func waitDone(t *testing.T, res *cmdResult, within time.Duration) protocol.Response {
	t.Helper()
	select {
	case d := <-res.done:
		return d
	case <-time.After(within):
		t.Fatalf("timed out waiting for done; saw: %+v", res.all())
		return protocol.Response{}
	}
}

func newTestController(t *testing.T) (*Controller, *Session) {
	t.Helper()
	base := t.TempDir()
	c := NewController(base)
	sess, err := NewSession(base)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	c.AddSession(sess)
	c.PrimarySID = sess.SID
	t.Cleanup(func() { sess.Kill() })
	return c, sess
}

func TestExecBasicAndRC(t *testing.T) {
	c, sess := newTestController(t)

	res := runCmd(t, c, sess, 1, "echo hello-world", false, 10*time.Second, 5*time.Second)
	done := waitDone(t, res, 5*time.Second)
	if done.RC == nil || *done.RC != 0 {
		t.Fatalf("expected rc 0, got %v", done.RC)
	}

	lines, _ := sess.ReadOutput(1, "stdout", 0, 0)
	if len(lines) != 1 || lines[0] != "hello-world" {
		t.Fatalf("stdout mismatch: %v", lines)
	}

	// Non-zero exit code is captured.
	res2 := runCmd(t, c, sess, 2, "bash -c 'exit 7'", false, 10*time.Second, 5*time.Second)
	done2 := waitDone(t, res2, 5*time.Second)
	if done2.RC == nil || *done2.RC != 7 {
		t.Fatalf("expected rc 7, got %v", done2.RC)
	}
}

func TestCWDTrackingNoPTYPollution(t *testing.T) {
	c, sess := newTestController(t)

	res := runCmd(t, c, sess, 1, "cd /tmp && pwd", false, 10*time.Second, 5*time.Second)
	done := waitDone(t, res, 5*time.Second)
	if done.CWD != "/tmp" {
		t.Fatalf("expected cwd /tmp, got %q", done.CWD)
	}

	// CWD persists to the next command and is captured deterministically.
	res2 := runCmd(t, c, sess, 2, "pwd", false, 10*time.Second, 5*time.Second)
	done2 := waitDone(t, res2, 5*time.Second)
	if done2.CWD != "/tmp" {
		t.Fatalf("expected cwd to persist as /tmp, got %q", done2.CWD)
	}
	out, _ := sess.ReadOutput(2, "stdout", 0, 0)
	if len(out) != 1 || out[0] != "/tmp" {
		t.Fatalf("pwd output mismatch: %v", out)
	}

	// The wrapper must not leak `pwd > .cwd` bookkeeping into peek output.
	for _, line := range sess.Peek(50) {
		if strings.Contains(line, ".cwd") || strings.Contains(line, ".wrapper_ready") {
			t.Errorf("peek leaked wrapper bookkeeping: %q", line)
		}
	}
}

func TestBackgroundProcessSurfaced(t *testing.T) {
	c, sess := newTestController(t)

	// The foreground command returns immediately; the bg sleep keeps running.
	res := runCmd(t, c, sess, 1, "sleep 8 & echo started", false, 10*time.Second, 5*time.Second)
	done := waitDone(t, res, 5*time.Second)
	if done.RC == nil || *done.RC != 0 {
		t.Fatalf("expected rc 0, got %v", done.RC)
	}
	if len(done.BackgroundPIDs) == 0 {
		t.Fatalf("expected a background pid to be surfaced for `sleep 8 &`, got none")
	}
}

func TestExitRestartsSession(t *testing.T) {
	c, sess := newTestController(t)

	res := runCmd(t, c, sess, 1, "exit 3", false, 10*time.Second, 6*time.Second)
	done := waitDone(t, res, 6*time.Second)
	if done.Error == "" {
		t.Fatalf("expected an error explaining the shell exited, got none")
	}

	// After auto-restart the session must be usable again under the same SID.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sess.IsAlive() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !sess.IsAlive() {
		t.Fatalf("session should be alive after restart")
	}

	res2 := runCmd(t, c, sess, 2, "echo back-alive", false, 10*time.Second, 6*time.Second)
	done2 := waitDone(t, res2, 6*time.Second)
	if done2.RC == nil || *done2.RC != 0 {
		t.Fatalf("expected rc 0 after restart, got %v (err=%q)", done2.RC, done2.Error)
	}
	out, _ := sess.ReadOutput(2, "stdout", 0, 0)
	if len(out) != 1 || out[0] != "back-alive" {
		t.Fatalf("post-restart output mismatch: %v", out)
	}
}

func TestPromptDetectionAndAutoYes(t *testing.T) {
	c, sess := newTestController(t)

	// Without --yes: a stdin-reading command should be reported as a prompt.
	res := runCmd(t, c, sess, 1, `read -p "Continue? " x`, false, 8*time.Second, 8*time.Second)
	sawPrompt := false
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range res.all() {
			if r.Op == protocol.OpPrompt {
				sawPrompt = true
			}
		}
		if sawPrompt {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	// Unblock the read so the shell returns to a clean state.
	sess.writePTY([]byte("\n"))
	if !sawPrompt {
		t.Fatalf("expected a prompt for `read -p`, got: %+v", res.all())
	}

	// With --yes: the daemon auto-answers and the command completes.
	res2 := runCmd(t, c, sess, 2, `read -p "OK? " a && echo got=$a`, true, 10*time.Second, 10*time.Second)
	done2 := waitDone(t, res2, 9*time.Second)
	if done2.RC == nil || *done2.RC != 0 {
		t.Fatalf("expected --yes to complete the read, got rc=%v", done2.RC)
	}
	out, _ := sess.ReadOutput(2, "stdout", 0, 0)
	if len(out) == 0 || !strings.HasPrefix(out[0], "got=yes") {
		t.Fatalf("expected auto-yes answer, got %v", out)
	}
}

func TestSessionUsableAfterReadTimeout(t *testing.T) {
	c, sess := newTestController(t)

	// A `read` builtin blocks bash itself (no separate process group). A hard
	// timeout must still leave the session usable.
	res := runCmd(t, c, sess, 1, `read -p "x" v`, false, 2*time.Second, 8*time.Second)
	done := waitDone(t, res, 8*time.Second)
	if done.Error == "" {
		t.Fatalf("expected a timeout error, got rc=%v", done.RC)
	}

	res2 := runCmd(t, c, sess, 2, "echo after-read-timeout", false, 10*time.Second, 6*time.Second)
	done2 := waitDone(t, res2, 6*time.Second)
	if done2.RC == nil || *done2.RC != 0 {
		t.Fatalf("session wedged after read timeout: rc=%v err=%q", done2.RC, done2.Error)
	}
	out, _ := sess.ReadOutput(2, "stdout", 0, 0)
	if len(out) != 1 || out[0] != "after-read-timeout" {
		t.Fatalf("post-timeout output mismatch: %v", out)
	}
}

func TestSessionUsableAfterCommandTimeout(t *testing.T) {
	c, sess := newTestController(t)

	// An external command in its own foreground group is killed by group.
	res := runCmd(t, c, sess, 1, "sleep 30", false, 2*time.Second, 8*time.Second)
	waitDone(t, res, 8*time.Second)

	res2 := runCmd(t, c, sess, 2, "echo after-sleep-timeout", false, 10*time.Second, 6*time.Second)
	done2 := waitDone(t, res2, 6*time.Second)
	if done2.RC == nil || *done2.RC != 0 {
		t.Fatalf("session wedged after command timeout: rc=%v err=%q", done2.RC, done2.Error)
	}
}

func TestConcurrentSessionsNoCrosstalk(t *testing.T) {
	base := t.TempDir()
	c := NewController(base)

	const n = 4
	sessions := make([]*Session, n)
	for i := 0; i < n; i++ {
		s, err := NewSession(base)
		if err != nil {
			t.Fatalf("NewSession %d: %v", i, err)
		}
		c.AddSession(s)
		sessions[i] = s
		defer s.Kill()
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := "sess-" + sessions[i].SID
			res := runCmd(t, c, sessions[i], 1, "cd /tmp && sleep 1 && echo "+marker, false, 15*time.Second, 12*time.Second)
			d := waitDone(t, res, 12*time.Second)
			if d.RC == nil || *d.RC != 0 {
				errs[i] = errChk("rc", sessions[i].SID)
				return
			}
			out, _ := sessions[i].ReadOutput(1, "stdout", 0, 0)
			if len(out) != 1 || out[0] != marker {
				errs[i] = errChk("output "+strings.Join(out, "|"), sessions[i].SID)
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("session %d (%s): %v", i, sessions[i].SID, e)
		}
	}
}

func errChk(what, sid string) error { return &chkErr{what, sid} }

type chkErr struct{ what, sid string }

func (e *chkErr) Error() string { return "check failed: " + e.what + " for " + e.sid }
