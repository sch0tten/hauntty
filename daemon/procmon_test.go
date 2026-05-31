package daemon

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

// startTestBash launches an interactive bash on a PTY (matching how Session
// runs it) and returns the pid, the PTY master, and a cleanup func.
func startTestBash(t *testing.T) (int, *os.File, func()) {
	t.Helper()
	sh := exec.Command("/bin/bash", "--norc", "--noprofile")
	sh.Env = append(os.Environ(), "PS1=$ ", "TERM=xterm-256color")
	ptmx, err := pty.Start(sh)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})
	pid := sh.Process.Pid
	// Give bash a moment to set up job control on the PTY.
	time.Sleep(250 * time.Millisecond)
	cleanup := func() {
		sh.Process.Kill()
		ptmx.Close()
		sh.Wait()
	}
	return pid, ptmx, cleanup
}

// sampleUntil polls the monitor until pred is satisfied or the deadline passes.
// Returns the last status seen.
func sampleUntil(t *testing.T, pm *ProcMonitor, within time.Duration, pred func(*CommandStatus) bool) *CommandStatus {
	t.Helper()
	deadline := time.Now().Add(within)
	var last *CommandStatus
	for time.Now().Before(deadline) {
		st, err := pm.Sample()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		last = st
		if pred(st) {
			return st
		}
		time.Sleep(100 * time.Millisecond)
	}
	return last
}

func TestClassifyForegroundCommand(t *testing.T) {
	pid, ptmx, cleanup := startTestBash(t)
	defer cleanup()

	pm := NewProcMonitor(pid)
	// Prime bashPgrp.
	if _, err := pm.Sample(); err != nil {
		t.Fatalf("prime sample: %v", err)
	}

	ptmx.Write([]byte("sleep 3\n"))
	st := sampleUntil(t, pm, 2*time.Second, func(s *CommandStatus) bool {
		return s.Classification == ClassRunning || len(s.Foreground) > 0
	})
	if len(st.Foreground) == 0 {
		t.Fatalf("expected a foreground process for `sleep 3`, got class=%s fg=%v bg=%v",
			st.Classification, st.Foreground, st.Background)
	}
	if len(st.Background) != 0 {
		t.Errorf("expected no background jobs, got %v", st.BackgroundPIDs())
	}
}

func TestClassifyBackgroundJobNotForeground(t *testing.T) {
	pid, ptmx, cleanup := startTestBash(t)
	defer cleanup()

	pm := NewProcMonitor(pid)
	pm.Sample() // prime

	// Start a background job, then settle. bash returns to the foreground.
	ptmx.Write([]byte("sleep 30 &\n"))
	st := sampleUntil(t, pm, 2*time.Second, func(s *CommandStatus) bool {
		return len(s.Background) > 0
	})

	if len(st.Background) == 0 {
		t.Fatalf("expected the `&` job to be surfaced as background, got fg=%v bg=%v class=%s",
			st.Foreground, st.Background, st.Classification)
	}
	// The background job must NOT be counted as the foreground command, and the
	// shell should read as done/idle (foreground returned to the prompt).
	if len(st.Foreground) != 0 {
		t.Errorf("background job leaked into foreground set: %v", st.Foreground)
	}
	if st.Classification != ClassDone && st.Classification != ClassIdle {
		t.Errorf("shell with only a bg job should be done/idle, got %s", st.Classification)
	}
}

func TestClassifyReadBuiltinWaitsForInput(t *testing.T) {
	pid, ptmx, cleanup := startTestBash(t)
	defer cleanup()

	pm := NewProcMonitor(pid)
	pm.Sample() // prime

	// `read` is a bash builtin: no child process, bash itself blocks on the tty.
	ptmx.Write([]byte("read -p 'Q? ' x\n"))
	st := sampleUntil(t, pm, 3*time.Second, func(s *CommandStatus) bool {
		return s.Classification == ClassDone || s.Classification == ClassWaitingInput
	})
	// With no children and bash blocked on a tty read, classify reports
	// ClassDone (the controller's rc-file poll + debounce promotes this to a
	// prompt). The critical property: it is NOT stuck as idle/running.
	if st.Classification != ClassDone && st.Classification != ClassWaitingInput {
		t.Fatalf("read builtin should classify as done/waiting_input, got %s (fg=%v bg=%v)",
			st.Classification, st.Foreground, st.Background)
	}
}

func TestClassifyBackgroundDoesNotMaskReadPrompt(t *testing.T) {
	pid, ptmx, cleanup := startTestBash(t)
	defer cleanup()

	pm := NewProcMonitor(pid)
	pm.Sample() // prime

	// Reproduces the production bug: a lingering background job must not mask
	// detection of a subsequent `read` prompt.
	ptmx.Write([]byte("sleep 30 &\n"))
	time.Sleep(400 * time.Millisecond)
	ptmx.Write([]byte("read -p 'Q? ' x\n"))

	st := sampleUntil(t, pm, 3*time.Second, func(s *CommandStatus) bool {
		return s.Classification == ClassDone || s.Classification == ClassWaitingInput
	})
	if st.Classification != ClassDone && st.Classification != ClassWaitingInput {
		t.Fatalf("read prompt masked by background job: class=%s fg=%v bg=%v",
			st.Classification, st.Foreground, st.BackgroundPIDs())
	}
	if len(st.Background) == 0 {
		t.Errorf("expected the bg job to still be tracked as background")
	}
}

func TestClassifyZombieShell(t *testing.T) {
	pid, ptmx, cleanup := startTestBash(t)
	defer cleanup()

	pm := NewProcMonitor(pid)
	pm.Sample() // prime

	// `exit` terminates the shell; it becomes a zombie until reaped.
	ptmx.Write([]byte("exit 3\n"))
	st := sampleUntil(t, pm, 2*time.Second, func(s *CommandStatus) bool {
		return s.Classification == ClassZombie
	})
	if st.Classification != ClassZombie {
		t.Fatalf("exited shell should classify as zombie, got %s", st.Classification)
	}
}

func TestCPUPercentNoUnderflow(t *testing.T) {
	// Synthetic check: when the foreground PID set changes between samples, the
	// per-PID delta must never produce a wrapped/huge value.
	pm := NewProcMonitor(99999)
	pm.bashPgrp = 1
	pm.prevSampleAt = time.Now().Add(-1 * time.Second)
	pm.prevCPU = map[int]uint64{100: 5000} // old PID with high jiffies

	// New foreground set has a different PID with low jiffies.
	fg := []ProcessState{{PID: 200, Utime: 10, Stime: 5}}
	pct := pm.cpuPercent(fg, time.Now())
	if pct < 0 || pct > 1000 {
		t.Fatalf("cpuPercent underflow/overflow: got %v", pct)
	}
}
