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
	// If the rc file hasn't appeared within this window, the command is
	// long-running and needs /proc monitoring for prompt detection, status, etc.
	FastPhaseTimeout = 1000 * time.Millisecond

	// SlowPhaseTick is the polling interval during Phase 2 (/proc monitoring).
	SlowPhaseTick = 1 * time.Second
)

// Controller manages command execution and monitoring across sessions.
// It replaces the inline poll goroutine with /proc-based monitoring.
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
	mu             sync.Mutex // protects encoder writes
}

// safeEncode writes a response with mutex protection.
func (h *CommandHandle) safeEncode(resp *protocol.Response) error {
	h.mu.Lock()
	defer h.mu.Unlock()
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

	// Cancel all active commands for this session
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
		pid := 0
		if sess.Shell.Process != nil {
			pid = sess.Shell.Process.Pid
		}
		infos = append(infos, protocol.SessionInfo{
			SID:     sess.SID,
			User:    sess.User,
			Host:    sess.Hostname,
			IP:      sess.IP,
			PID:     pid,
			Created: sess.Created.Format(time.RFC3339),
			CWD:     sess.CWD,
			LastSeq: sess.seq,
			Primary: sess.SID == c.PrimarySID,
			Alive:   sess.IsAlive(),
		})
	}
	return infos
}

// ExecCommand starts a command and monitors it via /proc.
func (c *Controller) ExecCommand(sess *Session, seq int, cmd string, enc *protocol.Encoder, nonInteractive bool) {
	ctx, cancel := context.WithCancel(context.Background())

	bashPID := 0
	if sess.Shell.Process != nil {
		bashPID = sess.Shell.Process.Pid
	}

	handle := &CommandHandle{
		Seq:            seq,
		Cmd:            cmd,
		Session:        sess,
		Monitor:        NewProcMonitor(bashPID),
		StartTime:      time.Now(),
		Encoder:        enc,
		Cancel:         cancel,
		NonInteractive: nonInteractive,
	}

	key := commandKey{sid: sess.SID, seq: seq}
	c.mu.Lock()
	c.commands[key] = handle
	c.mu.Unlock()

	go c.monitorCommand(ctx, handle, key)
}

// monitorCommand watches a running command using a two-phase strategy:
//
// Phase 1 (fast): Poll rc file every FastPhaseTick (50ms) for up to FastPhaseTimeout (1s).
// No /proc overhead — fast commands (df -h, echo, etc.) complete and return in ~50ms.
//
// Phase 2 (slow): If rc file hasn't appeared, engage full /proc monitoring every SlowPhaseTick (1s).
// Handles prompt detection, status updates, zombie detection, wrapper recovery, etc.
func (c *Controller) monitorCommand(ctx context.Context, h *CommandHandle, key commandKey) {
	defer func() {
		c.mu.Lock()
		delete(c.commands, key)
		c.mu.Unlock()
	}()

	cmdDir := filepath.Join(h.Session.Dir, fmt.Sprintf("cmd.%d", h.Seq))
	stderrPath := filepath.Join(cmdDir, "stderr")

	// Phase 1: fast rc file polling
	if c.fastPhase(ctx, h, cmdDir, stderrPath) {
		return // command completed during fast phase
	}

	// Phase 2: full /proc monitoring
	c.slowPhase(ctx, h, key, cmdDir, stderrPath)
}

// fastPhase polls the rc file at high frequency for fast command completion.
// Returns true if the command completed (caller should return).
func (c *Controller) fastPhase(ctx context.Context, h *CommandHandle, cmdDir, stderrPath string) bool {
	ticker := time.NewTicker(FastPhaseTick)
	defer ticker.Stop()

	timeout := time.After(FastPhaseTimeout)

	for {
		select {
		case <-ctx.Done():
			return true

		case <-timeout:
			return false // switch to slow phase

		case <-ticker.C:
			done, rc, err := h.Session.Poll(h.Seq)
			if err != nil {
				return true
			}
			if done {
				c.handleDone(h, cmdDir, stderrPath, rc)
				return true
			}
		}
	}
}

// slowPhase engages full /proc monitoring for long-running commands.
func (c *Controller) slowPhase(ctx context.Context, h *CommandHandle, key commandKey, cmdDir, stderrPath string) {
	ticker := time.NewTicker(SlowPhaseTick)
	defer ticker.Stop()

	hardDeadline := time.After(10 * time.Minute)
	yesAlreadySent := false
	promptReported := false
	statusSent := false
	doneWithoutRC := 0

	for {
		select {
		case <-ctx.Done():
			return

		case <-hardDeadline:
			log.Printf("hard timeout for seq %d (cmd: %s), recovering", h.Seq, h.Cmd)
			h.Session.recoverShell()
			rc := -1
			h.safeEncode(&protocol.Response{
				Op:    protocol.OpDone,
				Seq:   h.Seq,
				RC:    &rc,
				Error: "hard timeout: command did not complete within 10 minutes, session recovered",
				CWD:   h.Session.CWD,
			})
			return

		case <-ticker.C:
			// 1. Check if command completed (rc file exists)
			done, rc, err := h.Session.Poll(h.Seq)
			if err != nil {
				return
			}
			if done {
				c.handleDone(h, cmdDir, stderrPath, rc)
				return
			}

			// 2. Sample /proc for process state
			status, err := h.Monitor.Sample()
			if err != nil {
				elapsed := time.Since(h.StartTime)
				if elapsed > FastPhaseTimeout {
					log.Printf("seq %d: bash process gone (pid %d)", h.Seq, h.Monitor.bashPID)
					rc := -1
					h.safeEncode(&protocol.Response{
						Op:    protocol.OpDone,
						Seq:   h.Seq,
						RC:    &rc,
						Error: "shell process exited (command may have called 'exit'); session needs reconnect",
						CWD:   h.Session.CWD,
					})
					return
				}
				continue
			}

			elapsed := time.Since(h.StartTime)

			// 2b. Bash zombie — shell exited (e.g., "exit N")
			if status.Classification == ClassZombie {
				log.Printf("seq %d: bash is zombie (pid %d), command killed the shell", h.Seq, h.Monitor.bashPID)
				rc := -1
				h.safeEncode(&protocol.Response{
					Op:    protocol.OpDone,
					Seq:   h.Seq,
					RC:    &rc,
					Error: "shell process exited (command may have called 'exit'); session needs reconnect",
					CWD:   h.Session.CWD,
				})
				return
			}

			// 3. Check for wrapper failure (within first 5s of slow phase)
			if elapsed < 6*time.Second && elapsed > 3*time.Second {
				if data, rerr := os.ReadFile(stderrPath); rerr == nil {
					if strings.Contains(string(data), "__hauntty_exec: command not found") {
						log.Printf("wrapper not found for seq %d, recovering", h.Seq)
						h.Session.recoverShell()
						rc := -1
						h.safeEncode(&protocol.Response{
							Op:    protocol.OpDone,
							Seq:   h.Seq,
							RC:    &rc,
							Error: "wrapper function not found, session recovered",
							CWD:   h.Session.CWD,
						})
						return
					}
				}
			}

			// 4. Handle waiting_input — deterministic via /proc wchan
			if status.Classification == ClassDone {
				doneWithoutRC++
			} else {
				doneWithoutRC = 0
			}
			waitingInput := status.Classification == ClassWaitingInput || doneWithoutRC >= 2
			if waitingInput {
				if h.NonInteractive && !yesAlreadySent {
					log.Printf("seq %d waiting for input, auto-answering (non-interactive)", h.Seq)
					h.Session.PTY.Write([]byte("yes\n"))
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
			}

			// 5. Send periodic status (every ~3s, not when waiting for input)
			if elapsed > 2*time.Second && !waitingInput {
				if !statusSent || int(elapsed.Seconds())%3 == 0 {
					childPID := 0
					if len(status.Children) > 0 {
						childPID = status.Children[0].PID
					}
					h.safeEncode(&protocol.Response{
						Op:       protocol.OpStatus,
						Seq:      h.Seq,
						State:    string(status.Classification),
						CPU:      status.CPUPct,
						IOBytes:  status.IOReadBytes + status.IOWriteBytes,
						Elapsed:  elapsed.Seconds(),
						ChildPID: childPID,
					})
					statusSent = true
				}
			}
		}
	}
}

// handleDone processes a completed command (rc file found).
func (c *Controller) handleDone(h *CommandHandle, cmdDir, stderrPath string, rc int) {
	h.Session.UpdateCWD()

	stdoutPath := filepath.Join(cmdDir, "stdout")
	stdoutLines := CountLines(stdoutPath)
	stderrLines := CountLines(stderrPath)

	// Check for wrapper failure
	if stderrLines > 0 {
		stderrData, _ := os.ReadFile(stderrPath)
		if strings.Contains(string(stderrData), "__hauntty_exec: command not found") {
			log.Printf("wrapper lost for seq %d, recovering", h.Seq)
			h.Session.recoverShell()
		}
	}

	elapsed := time.Since(h.StartTime)

	AppendSessionLog(h.Session.Dir, h.Seq, h.Cmd, rc, h.Session.CWD, stdoutLines, stderrLines)

	if err := AppendCorpusEntry(c.baseDir, h.Session, h.Seq, h.Cmd, rc, h.Session.CWD, stdoutLines, stderrLines, elapsed); err != nil {
		log.Printf("corpus write error for seq %d: %v", h.Seq, err)
	}

	h.safeEncode(&protocol.Response{
		Op:          protocol.OpDone,
		Seq:         h.Seq,
		RC:          &rc,
		StdoutLines: stdoutLines,
		StderrLines: stderrLines,
		CWD:         h.Session.CWD,
	})
}

// SendInput writes text to a session's PTY (for interactive commands).
func (c *Controller) SendInput(sess *Session, input string) error {
	_, err := sess.PTY.Write([]byte(input))
	return err
}
