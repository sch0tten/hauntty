package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sch0tten/hauntty/protocol"
)

const (
	// FastPhaseTick is the polling interval during Phase 1 (rc file watch only).
	// Fast commands (df -h, echo, etc.) complete within a few ms and are detected
	// at the next tick — no /proc overhead.
	FastPhaseTick = 50 * time.Millisecond

	// FastPhaseTimeout is how long Phase 1 runs before switching to Phase 2.
	FastPhaseTimeout = 1000 * time.Millisecond

	// SlowPhaseTick is the polling interval during Phase 2 (/proc monitoring).
	SlowPhaseTick = 1 * time.Second

	// DefaultHardDeadline bounds a command when the client does not specify a
	// per-exec timeout. Generous for ops workloads (builds, migrations); a
	// runaway command is terminated deterministically by its process group.
	DefaultHardDeadline = 10 * time.Minute

	// StatusInterval is how often live status is emitted for a long command.
	StatusInterval = 3 * time.Second
)

// Controller manages command execution and monitoring across sessions.
type Controller struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	commands   map[commandKey]*CommandHandle
	baseDir    string
	PrimarySID string // SID of the first session, used as default
}

type commandKey struct {
	sid string
	seq int
}

// CommandHandle tracks an active command being monitored.
type CommandHandle struct {
	Seq            int
	Cmd            string
	Session        *Session
	Monitor        *ProcMonitor
	StartTime      time.Time
	Encoder        *protocol.Encoder
	Cancel         context.CancelFunc
	NonInteractive bool
	Deadline       time.Duration
}

// safeEncode writes a response. The encoder is connection-safe, so concurrent
// writers (this monitor, sibling monitors, the request loop) never interleave.
func (h *CommandHandle) safeEncode(resp *protocol.Response) error {
	return h.Encoder.Encode(resp)
}

// NewController creates a new controller.
func NewController(baseDir string) *Controller {
	return &Controller{
		sessions: make(map[string]*Session),
		commands: make(map[commandKey]*CommandHandle),
		baseDir:  baseDir,
	}
}

// AddSession registers a session with the controller.
func (c *Controller) AddSession(sess *Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sess.SID] = sess
}

// AddSessionLimited registers a session only if the count is below max,
// enforcing the cap atomically (no check-then-add race across spawns).
func (c *Controller) AddSessionLimited(sess *Session, max int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sessions) >= max {
		return fmt.Errorf("session limit reached (%d/%d)", len(c.sessions), max)
	}
	c.sessions[sess.SID] = sess
	return nil
}

// GetSession returns a session by SID.
func (c *Controller) GetSession(sid string) (*Session, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.sessions[sid]
	return s, ok
}

// RemoveSession removes and kills a session.
func (c *Controller) RemoveSession(sid string) error {
	c.mu.Lock()
	sess, ok := c.sessions[sid]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("session not found: %s", sid)
	}
	delete(c.sessions, sid)

	for key, handle := range c.commands {
		if key.sid == sid {
			handle.Cancel()
			delete(c.commands, key)
		}
	}
	c.mu.Unlock()

	return sess.Kill()
}

// SessionCount returns the number of active sessions.
func (c *Controller) SessionCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sessions)
}

// ListSessions returns info about all sessions.
func (c *Controller) ListSessions() []protocol.SessionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var infos []protocol.SessionInfo
	for _, sess := range c.sessions {
		infos = append(infos, protocol.SessionInfo{
			SID:     sess.SID,
			User:    sess.User,
			Host:    sess.Hostname,
			IP:      sess.IP,
			PID:     sess.PID(),
			Created: sess.Created.Format(time.RFC3339),
			CWD:     sess.GetCWD(),
			LastSeq: sess.Seq(),
			Primary: sess.SID == c.PrimarySID,
			Alive:   sess.IsAlive(),
		})
	}
	return infos
}

// ExecCommand starts a command and monitors it via /proc.
func (c *Controller) ExecCommand(sess *Session, seq int, cmd string, enc *protocol.Encoder, nonInteractive bool, deadline time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())

	if deadline <= 0 {
		deadline = DefaultHardDeadline
	}

	handle := &CommandHandle{
		Seq:            seq,
		Cmd:            cmd,
		Session:        sess,
		Monitor:        NewProcMonitor(sess.PID()),
		StartTime:      time.Now(),
		Encoder:        enc,
		Cancel:         cancel,
		NonInteractive: nonInteractive,
		Deadline:       deadline,
	}

	key := commandKey{sid: sess.SID, seq: seq}
	c.mu.Lock()
	c.commands[key] = handle
	c.mu.Unlock()

	go c.monitorCommand(ctx, handle, key)
}

// monitorCommand watches a running command with a two-phase strategy.
func (c *Controller) monitorCommand(ctx context.Context, h *CommandHandle, key commandKey) {
	defer func() {
		c.mu.Lock()
		delete(c.commands, key)
		c.mu.Unlock()
	}()

	cmdDir := filepath.Join(h.Session.Dir, fmt.Sprintf("cmd.%d", h.Seq))
	stderrPath := filepath.Join(cmdDir, "stderr")

	if c.fastPhase(ctx, h, cmdDir, stderrPath) {
		return
	}
	c.slowPhase(ctx, h, cmdDir, stderrPath)
}

// fastPhase polls the rc file at high frequency for fast command completion.
func (c *Controller) fastPhase(ctx context.Context, h *CommandHandle, cmdDir, stderrPath string) bool {
	ticker := time.NewTicker(FastPhaseTick)
	defer ticker.Stop()
	timeout := time.After(FastPhaseTimeout)

	for {
		select {
		case <-ctx.Done():
			return true
		case <-timeout:
			return false
		case <-ticker.C:
			done, rc, err := h.Session.Poll(h.Seq)
			if err != nil {
				continue // transient read error — retry next tick
			}
			if done {
				c.handleDone(h, cmdDir, stderrPath, rc)
				return true
			}
		}
	}
}

// slowPhase engages full /proc monitoring for long-running commands.
func (c *Controller) slowPhase(ctx context.Context, h *CommandHandle, cmdDir, stderrPath string) {
	ticker := time.NewTicker(SlowPhaseTick)
	defer ticker.Stop()

	hardDeadline := time.After(h.Deadline)
	yesAlreadySent := false
	promptReported := false
	doneWithoutRC := 0
	sampleFailures := 0
	lastStatusAt := time.Time{}
	lastFgPgrp := 0

	for {
		select {
		case <-ctx.Done():
			return

		case <-hardDeadline:
			log.Printf("hard timeout (%.0fs) for seq %d (cmd: %s); killing foreground group %d",
				h.Deadline.Seconds(), h.Seq, h.Cmd, lastFgPgrp)
			h.Session.KillForeground(lastFgPgrp)
			// Give the wrapper a moment to write rc after the kill.
			time.Sleep(500 * time.Millisecond)
			if done, rc, _ := h.Session.Poll(h.Seq); done {
				c.handleDone(h, cmdDir, stderrPath, rc)
				return
			}
			// Killing the foreground group didn't resolve it — e.g. a bash
			// builtin like `read` blocks the shell itself, which has no separate
			// group to signal. Interrupt and re-establish the shell so the
			// session stays usable rather than wedged on the dead command.
			h.Session.recoverShell()
			rc := -1
			h.safeEncode(&protocol.Response{
				Op:    protocol.OpDone,
				Seq:   h.Seq,
				RC:    &rc,
				Error: fmt.Sprintf("hard timeout: command exceeded %.0fs and was terminated", h.Deadline.Seconds()),
				CWD:   h.Session.GetCWD(),
			})
			return

		case <-ticker.C:
			// 1. Completion?
			done, rc, err := h.Session.Poll(h.Seq)
			if err != nil {
				continue
			}
			if done {
				c.handleDone(h, cmdDir, stderrPath, rc)
				return
			}

			// 2. Honor "serialize within session": if an earlier command on this
			// session is still running, this one is queued in the shell. Don't
			// sample /proc (it would observe the other command's foreground) or
			// emit status — just wait our turn. The hard deadline counts the
			// queue wait too, which is intended (total wall-clock budget).
			if !h.Session.IsOldestRunning(h.Seq) {
				continue
			}

			// 3. Sample /proc.
			status, err := h.Monitor.Sample()
			if err != nil {
				sampleFailures++
				if sampleFailures >= 3 && !h.Session.shellRunning() {
					c.onShellDeath(h)
					return
				}
				continue
			}
			sampleFailures = 0

			// Track the current foreground group for a potential deadline kill.
			if len(status.Foreground) > 0 {
				lastFgPgrp = status.Foreground[0].Pgrp
			} else if status.BashState.Tpgid > 0 {
				lastFgPgrp = status.BashState.Tpgid
			}

			// 4. Shell died (zombie) — restart so the SID stays usable.
			if status.Classification == ClassZombie || !h.Session.shellRunning() {
				c.onShellDeath(h)
				return
			}

			// 5. Wrapper lost (rare now that injection is verified) — recover.
			if h.Monitor.startTime.Add(6*time.Second).After(time.Now()) &&
				time.Since(h.StartTime) > 3*time.Second {
				if data, rerr := os.ReadFile(stderrPath); rerr == nil &&
					strings.Contains(string(data), "__hauntty_exec: command not found") {
					log.Printf("wrapper not found for seq %d, recovering", h.Seq)
					h.Session.recoverShell()
					rc := -1
					h.safeEncode(&protocol.Response{
						Op:    protocol.OpDone,
						Seq:   h.Seq,
						RC:    &rc,
						Error: "wrapper function not found, session recovered",
						CWD:   h.Session.GetCWD(),
					})
					return
				}
			}

			// 6. Waiting for input — deterministic via foreground tty wait, or a
			// bash `read` builtin (ClassDone with no rc, debounced).
			if status.Classification == ClassDone {
				doneWithoutRC++
			} else {
				doneWithoutRC = 0
			}
			waitingInput := status.Classification == ClassWaitingInput || doneWithoutRC >= 2
			if waitingInput {
				if h.NonInteractive && !yesAlreadySent {
					log.Printf("seq %d waiting for input, auto-answering (non-interactive)", h.Seq)
					h.Session.writePTY([]byte("yes\n"))
					yesAlreadySent = true
				} else if !h.NonInteractive && !promptReported {
					log.Printf("seq %d waiting for input, reporting to client", h.Seq)
					h.safeEncode(&protocol.Response{
						Op:     protocol.OpPrompt,
						Seq:    h.Seq,
						Prompt: "process waiting for terminal input (detected via /proc)",
						State:  protocol.StateWaitingInput,
					})
					promptReported = true
				}
				continue
			}

			// 7. Periodic status.
			if time.Since(h.StartTime) > FastPhaseTimeout &&
				time.Since(lastStatusAt) >= StatusInterval {
				childPID := 0
				if len(status.Foreground) > 0 {
					childPID = status.Foreground[0].PID
				}
				h.safeEncode(&protocol.Response{
					Op:             protocol.OpStatus,
					Seq:            h.Seq,
					State:          string(status.Classification),
					CPU:            status.CPUPct,
					IOBytes:        status.IOReadBytes + status.IOWriteBytes,
					Elapsed:        time.Since(h.StartTime).Seconds(),
					ChildPID:       childPID,
					BackgroundPIDs: status.BackgroundPIDs(),
				})
				lastStatusAt = time.Now()
			}
		}
	}
}

// onShellDeath reports the in-flight command as failed and rebuilds the shell
// in place so the session ID stays usable. The agent is told explicitly that
// the shell was reset (cwd/env lost) rather than silently continuing.
func (c *Controller) onShellDeath(h *CommandHandle) {
	log.Printf("seq %d: shell for session %s exited; restarting", h.Seq, h.Session.SID)
	rc := -1
	msg := "shell process exited (the command may have called 'exit'); session shell was reset — cwd and environment are lost"
	if err := h.Session.Restart(); err != nil {
		h.Session.MarkDead()
		log.Printf("session %s restart failed: %v", h.Session.SID, err)
		msg = "shell process exited and could not be restarted; spawn a new session"
	}
	h.safeEncode(&protocol.Response{
		Op:    protocol.OpDone,
		Seq:   h.Seq,
		RC:    &rc,
		Error: msg,
		CWD:   h.Session.GetCWD(),
	})
}

// handleDone processes a completed command (rc file found).
func (c *Controller) handleDone(h *CommandHandle, cmdDir, stderrPath string, rc int) {
	cwd := h.Session.LoadCWD(h.Seq)

	stdoutPath := filepath.Join(cmdDir, "stdout")
	stdoutLines := CountLines(stdoutPath)
	stderrLines := CountLines(stderrPath)

	if stderrLines > 0 {
		if stderrData, err := os.ReadFile(stderrPath); err == nil &&
			strings.Contains(string(stderrData), "__hauntty_exec: command not found") {
			log.Printf("wrapper lost for seq %d, recovering", h.Seq)
			h.Session.recoverShell()
		}
	}

	// Surface any background jobs the command left running, so the caller has a
	// deterministic handle instead of guessing whether work is still in flight.
	var bgPIDs []int
	if status, err := h.Monitor.Sample(); err == nil {
		bgPIDs = status.BackgroundPIDs()
	}

	elapsed := time.Since(h.StartTime)
	AppendSessionLog(h.Session.Dir, h.Seq, h.Cmd, rc, cwd, stdoutLines, stderrLines)
	if err := AppendCorpusEntry(c.baseDir, h.Session, h.Seq, h.Cmd, rc, cwd, stdoutLines, stderrLines, elapsed); err != nil {
		log.Printf("corpus write error for seq %d: %v", h.Seq, err)
	}

	h.safeEncode(&protocol.Response{
		Op:             protocol.OpDone,
		Seq:            h.Seq,
		RC:             &rc,
		StdoutLines:    stdoutLines,
		StderrLines:    stderrLines,
		CWD:            cwd,
		BackgroundPIDs: bgPIDs,
	})
}

// SendInput writes text to a session's PTY (for interactive commands).
func (c *Controller) SendInput(sess *Session, input string) error {
	return sess.writePTY([]byte(input))
}

// Shutdown cancels all active commands and kills all sessions.
func (c *Controller) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, handle := range c.commands {
		handle.Cancel()
	}
	for _, sess := range c.sessions {
		sess.Kill()
	}
}
