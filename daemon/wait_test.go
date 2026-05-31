package daemon

import (
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/sch0tten/hauntty/protocol"
)

// waitResp runs handleWait against an in-memory connection and returns the
// single response it emits.
func waitResp(t *testing.T, d *Daemon, sess *Session, req *protocol.Request, within time.Duration) protocol.Response {
	t.Helper()
	srv, cli := net.Pipe()
	defer srv.Close()
	defer cli.Close()
	enc := protocol.NewEncoder(srv)
	dec := protocol.NewDecoder(cli)

	out := make(chan protocol.Response, 1)
	go func() {
		var r protocol.Response
		if err := dec.Decode(&r); err == nil {
			out <- r
		}
	}()
	go d.handleWait(enc, sess, req)

	select {
	case r := <-out:
		return r
	case <-time.After(within):
		t.Fatalf("handleWait did not respond within %v", within)
		return protocol.Response{}
	}
}

func TestWaitOnPID(t *testing.T) {
	d := &Daemon{}
	c, sess := newTestController(t)
	_ = c

	// A real OS process we can watch exit.
	proc := exec.Command("sleep", "1")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := proc.Process.Pid
	go proc.Wait() // reap when it exits

	start := time.Now()
	r := waitResp(t, d, sess, &protocol.Request{Op: protocol.OpWait, WaitPID: pid, TimeoutS: 10}, 5*time.Second)
	if r.State != "exited" {
		t.Fatalf("expected state exited, got %q", r.State)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Errorf("returned too early (%v) — should have blocked until the process exited", elapsed)
	}
}

func TestWaitOnPIDTimeout(t *testing.T) {
	d := &Daemon{}
	c, sess := newTestController(t)
	_ = c

	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() { proc.Process.Kill(); proc.Wait() }()

	r := waitResp(t, d, sess, &protocol.Request{Op: protocol.OpWait, WaitPID: proc.Process.Pid, TimeoutS: 1}, 5*time.Second)
	if r.State != "timeout" {
		t.Fatalf("expected timeout, got %q", r.State)
	}
}

func TestWaitOnSeq(t *testing.T) {
	d := &Daemon{}
	c, sess := newTestController(t)

	// Kick off a ~2s command, then gate on its completion via wait --seq.
	res := runCmd(t, c, sess, 1, "sleep 2 && echo seq-done", false, 15*time.Second, 12*time.Second)

	r := waitResp(t, d, sess, &protocol.Request{Op: protocol.OpWait, Seq: 1, TimeoutS: 12}, 12*time.Second)
	if r.State != protocol.StateDone {
		t.Fatalf("expected done, got state=%q", r.State)
	}
	if r.RC == nil || *r.RC != 0 {
		t.Fatalf("expected rc 0, got %v", r.RC)
	}
	waitDone(t, res, 5*time.Second) // drain the command monitor
}
