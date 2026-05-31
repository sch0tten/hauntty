package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// WrapperReadyTimeout bounds how long we wait for the shell to source the exec
// wrapper. Generous so a loaded host doesn't trip a false failure.
const WrapperReadyTimeout = 5 * time.Second

// Session represents a persistent shell session with a PTY.
type Session struct {
	SID      string
	User     string
	Hostname string
	IP       string
	Created  time.Time
	Dir      string // ~/.hauntty/<sid>

	mu         sync.Mutex // guards seq, cwd, dead, running, shell, restarting, killed
	seq        int
	cwd        string
	dead       bool
	restarting bool
	killed     bool
	running    map[int]*CmdState // seq -> state
	shell      *exec.Cmd

	ptyMu  sync.Mutex // guards pty (swapped on Restart) and serializes writes
	pty    *os.File

	ptyBuf   *RingBuffer // circular buffer of recent PTY output
	watchers []io.Writer // spectators receiving raw PTY bytes
	watchMu  sync.Mutex
}

// CmdState tracks a running or completed command.
type CmdState struct {
	Seq       int
	Cmd       string
	StartTime time.Time
	Done      bool
	RC        int
}

// RingBuffer is a simple circular buffer for PTY output lines.
type RingBuffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
	pos   int
	full  bool
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		lines: make([]string, capacity),
		cap:   capacity,
	}
}

func (rb *RingBuffer) Write(line string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.lines[rb.pos] = line
	rb.pos = (rb.pos + 1) % rb.cap
	if rb.pos == 0 {
		rb.full = true
	}
}

func (rb *RingBuffer) LastN(n int) []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	total := rb.pos
	if rb.full {
		total = rb.cap
	}
	if n > total {
		n = total
	}

	result := make([]string, n)
	start := rb.pos - n
	if start < 0 {
		if rb.full {
			start += rb.cap
		} else {
			start = 0
			n = rb.pos
			result = make([]string, n)
		}
	}

	for i := 0; i < n; i++ {
		idx := (start + i) % rb.cap
		result[i] = rb.lines[idx]
	}
	return result
}

func generateSID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to a time-free deterministic value
		// rather than risk an all-zero SID colliding.
		return "ffffffff"
	}
	return hex.EncodeToString(b)
}

func randToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// startShell launches a fresh bash on a new PTY for the given session dir.
func startShell(sid string) (*exec.Cmd, *os.File, error) {
	shell := exec.Command("/bin/bash", "--norc", "--noprofile")
	shell.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PS1=$ ",
		fmt.Sprintf("HAUNTTY_SID=%s", sid),
	)
	ptmx, err := pty.Start(shell)
	if err != nil {
		return nil, nil, fmt.Errorf("start pty: %w", err)
	}
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})
	return shell, ptmx, nil
}

// NewSession creates a new persistent shell session.
func NewSession(baseDir string) (*Session, error) {
	sid := generateSID()
	dir := filepath.Join(baseDir, sid)

	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	shell, ptmx, err := startShell(sid)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	hostname, _ := os.Hostname()
	user := os.Getenv("USER")

	// SSH_CONNECTION = "client_ip client_port server_ip server_port"
	var serverIP string
	if sshConn := os.Getenv("SSH_CONNECTION"); sshConn != "" {
		parts := strings.Fields(sshConn)
		if len(parts) >= 3 {
			serverIP = parts[2]
		}
	}

	s := &Session{
		SID:      sid,
		User:     user,
		Hostname: hostname,
		IP:       serverIP,
		Created:  time.Now().UTC(),
		Dir:      dir,
		shell:    shell,
		pty:      ptmx,
		cwd:      os.Getenv("HOME"),
		running:  make(map[int]*CmdState),
		ptyBuf:   NewRingBuffer(1000),
	}

	s.writePIDFile()

	// Initialize session log
	os.WriteFile(filepath.Join(dir, "session.log"), []byte(""), 0644)

	// Start PTY reader bound to this PTY instance.
	go s.readPTY(ptmx)

	// Inject the exec wrapper and confirm the shell sourced it (no fixed sleep).
	if err := s.injectWrapper(); err != nil {
		shell.Process.Kill()
		ptmx.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("wrapper injection: %w", err)
	}

	return s, nil
}

func (s *Session) writePIDFile() {
	pid := 0
	if s.shell != nil && s.shell.Process != nil {
		pid = s.shell.Process.Pid
	}
	os.WriteFile(filepath.Join(s.Dir, "hauntty.pid"), []byte(fmt.Sprintf("%d", pid)), 0644)
}

// injectWrapper writes the __hauntty_exec function into the shell and blocks
// until the shell confirms it sourced the wrapper (by writing a nonce to a
// ready file). This replaces the old fixed 100ms sleep, which raced on loaded
// hosts and left the first command failing with "command not found".
func (s *Session) injectWrapper() error {
	token := randToken()
	wrapperPath := filepath.Join(s.Dir, ".hauntty_wrapper.sh")
	readyPath := filepath.Join(s.Dir, ".wrapper_ready")
	os.Remove(readyPath)

	// The wrapper captures the command's working directory (into cmd_dir/cwd,
	// written before rc so a reader that sees rc is guaranteed a cwd) and
	// removes the pending file once consumed. The final printf publishes the
	// readiness nonce.
	wrapper := fmt.Sprintf(`__hauntty_dir=%q

__hauntty_exec() {
    local seq="$1"
    local cmd_dir="${__hauntty_dir}/cmd.${seq}"
    local pending="${__hauntty_dir}/cmd.${seq}.pending"
    local cmd
    cmd="$(cat "$pending")"
    mkdir -p "${cmd_dir}"
    cp "$pending" "${cmd_dir}/cmdline"
    echo "### CMD ${seq} [$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] ${cmd} ###"
    exec 3>&1 4>&2
    exec 1>"${cmd_dir}/stdout" 2>"${cmd_dir}/stderr"
    eval "$cmd"
    local rc=$?
    pwd > "${cmd_dir}/cwd"
    exec 1>&3 2>&4
    exec 3>&- 4>&-
    cat "${cmd_dir}/stdout"
    if [ -s "${cmd_dir}/stderr" ]; then
        cat "${cmd_dir}/stderr" >&2
    fi
    rm -f "$pending"
    echo "$rc" > "${cmd_dir}/rc"
    echo "### END ${seq} RC=${rc} [$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] ###"
    return $rc
}
printf '%%s' %q > %q
`, s.Dir, token, readyPath)

	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0600); err != nil {
		return fmt.Errorf("write wrapper: %w", err)
	}
	if err := s.writePTY([]byte(fmt.Sprintf("source %s\n", wrapperPath))); err != nil {
		return fmt.Errorf("source wrapper: %w", err)
	}

	deadline := time.Now().Add(WrapperReadyTimeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(readyPath); err == nil && strings.TrimSpace(string(data)) == token {
			return nil
		}
		if !s.shellRunning() {
			return fmt.Errorf("shell exited before wrapper was ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("wrapper not ready within %v", WrapperReadyTimeout)
}

// writePTY serializes writes to the PTY and is safe across a Restart swap.
func (s *Session) writePTY(b []byte) error {
	s.ptyMu.Lock()
	defer s.ptyMu.Unlock()
	if s.pty == nil {
		return fmt.Errorf("pty closed")
	}
	_, err := s.pty.Write(b)
	return err
}

// readPTY continuously reads from the given PTY and distributes output. It is
// bound to a specific PTY instance so a Restart (which closes the old PTY and
// opens a new one) cleanly retires the old reader.
func (s *Session) readPTY(ptmx *os.File) {
	buf := make([]byte, 4096)
	var lineBuf strings.Builder

	for {
		n, err := ptmx.Read(buf)
		if err != nil {
			return
		}
		chunk := buf[:n]

		// Distribute to spectators (non-blocking: a stuck spectator must never
		// stall the PTY drain, which would back-pressure and freeze the shell).
		s.watchMu.Lock()
		alive := s.watchers[:0]
		for _, w := range s.watchers {
			if writeNonBlocking(w, chunk) {
				alive = append(alive, w)
			}
		}
		s.watchers = alive
		s.watchMu.Unlock()

		// Buffer lines for peek (strip ANSI escape codes).
		for _, b := range chunk {
			if b == '\n' {
				s.ptyBuf.Write(StripANSI(lineBuf.String()))
				lineBuf.Reset()
			} else if b == '\r' {
				// skip carriage returns
			} else {
				lineBuf.WriteByte(b)
			}
		}
	}
}

// writeNonBlocking writes to a spectator with a short deadline when possible,
// so a slow/blocked watcher is dropped instead of stalling the session.
func writeNonBlocking(w io.Writer, b []byte) bool {
	type deadlineWriter interface{ SetWriteDeadline(time.Time) error }
	if dw, ok := w.(deadlineWriter); ok {
		dw.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		_, err := w.Write(b)
		dw.SetWriteDeadline(time.Time{})
		return err == nil
	}
	_, err := w.Write(b)
	return err == nil
}

// NextSeq returns the next command sequence number.
func (s *Session) NextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Seq returns the current (last-assigned) sequence number.
func (s *Session) Seq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// BumpSeq raises the sequence counter to at least n (used for explicit seqs).
func (s *Session) BumpSeq(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > s.seq {
		s.seq = n
	}
}

// Exec sends a command to the shell via the exec wrapper.
func (s *Session) Exec(seq int, cmd string) error {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return fmt.Errorf("session shell has exited; spawn a new session or reconnect")
	}
	s.running[seq] = &CmdState{Seq: seq, Cmd: cmd, StartTime: time.Now().UTC()}
	s.mu.Unlock()

	// Write command to a pending file (0600 — may contain secrets); the wrapper
	// reads it directly, preserving shell metacharacters, and removes it after.
	cmdFile := filepath.Join(s.Dir, fmt.Sprintf("cmd.%d.pending", seq))
	if err := os.WriteFile(cmdFile, []byte(cmd), 0600); err != nil {
		return fmt.Errorf("write cmd file: %w", err)
	}
	return s.writePTY([]byte(fmt.Sprintf("__hauntty_exec %d\n", seq)))
}

// Poll checks if a command has completed by looking for the rc file.
func (s *Session) Poll(seq int) (done bool, rc int, err error) {
	rcPath := filepath.Join(s.Dir, fmt.Sprintf("cmd.%d", seq), "rc")
	data, err := os.ReadFile(rcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}

	var code int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &code)

	s.mu.Lock()
	if cs, ok := s.running[seq]; ok {
		cs.Done = true
		cs.RC = code
	}
	s.mu.Unlock()

	return true, code, nil
}

// ReadOutput reads lines from a command's stdout or stderr file.
func (s *Session) ReadOutput(seq int, stream string, offset, limit int) ([]string, error) {
	path := filepath.Join(s.Dir, fmt.Sprintf("cmd.%d", seq), stream)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if offset >= len(lines) {
		return []string{}, nil
	}
	lines = lines[offset:]
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}

	return lines, nil
}

// Peek returns the last N lines of PTY output, filtering wrapper bookkeeping.
func (s *Session) Peek(n int) []string {
	raw := s.ptyBuf.LastN(s.ptyBuf.cap)
	filtered := make([]string, 0, len(raw))
	for _, line := range raw {
		if isWrapperNoise(line) {
			continue
		}
		filtered = append(filtered, line)
	}
	if n < len(filtered) {
		filtered = filtered[len(filtered)-n:]
	}
	return filtered
}

// isWrapperNoise reports whether a PTY line is internal wrapper bookkeeping
// that should be hidden from peek output.
func isWrapperNoise(line string) bool {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "### CMD "), strings.HasPrefix(t, "### END "):
		return true
	case strings.HasPrefix(t, "$ source ") && strings.Contains(t, ".hauntty_wrapper.sh"):
		return true
	case strings.HasPrefix(t, "$ __hauntty_exec "):
		return true
	case strings.HasPrefix(t, "__hauntty_dir="):
		return true
	}
	return false
}

// AddWatcher adds a spectator writer.
func (s *Session) AddWatcher(w io.Writer) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.watchers = append(s.watchers, w)
}

// GetCWD returns the last known working directory.
func (s *Session) GetCWD() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// SetCWD updates the cached working directory.
func (s *Session) SetCWD(cwd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cwd = cwd
}

// LoadCWD reads the cwd the wrapper captured for a completed command and
// updates the session's cached CWD. Deterministic: the wrapper writes cwd
// before rc, so once a command is done the file exists.
func (s *Session) LoadCWD(seq int) string {
	cwdPath := filepath.Join(s.Dir, fmt.Sprintf("cmd.%d", seq), "cwd")
	if data, err := os.ReadFile(cwdPath); err == nil {
		cwd := strings.TrimSpace(string(data))
		if cwd != "" {
			s.SetCWD(cwd)
		}
	}
	return s.GetCWD()
}

// PID returns the current shell PID (0 if unavailable).
func (s *Session) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shell != nil && s.shell.Process != nil {
		return s.shell.Process.Pid
	}
	return 0
}

// shellRunning reports whether the shell process exists (zombie counts as not
// running for our purposes). Reads /proc state rather than relying on signal 0,
// which succeeds against zombies and would mask an exited shell.
func (s *Session) shellRunning() bool {
	pid := s.PID()
	if pid == 0 {
		return false
	}
	st, err := readProcessState(pid)
	if err != nil {
		return false
	}
	return st.State != 'Z'
}

// IsAlive reports whether the session can still run commands.
func (s *Session) IsAlive() bool {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	return s.shellRunning()
}

// MarkDead flags the session as no longer usable.
func (s *Session) MarkDead() {
	s.mu.Lock()
	s.dead = true
	s.mu.Unlock()
}

// Dead reports whether the session has been marked dead.
func (s *Session) Dead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}

// IsOldestRunning reports whether seq is the earliest still-running command on
// this session. Commands serialize within a session (the shell runs them in
// FIFO order), so a later command is queued until earlier ones finish; the
// monitor uses this to avoid observing another command's foreground group.
func (s *Session) IsOldestRunning(seq int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sq, cs := range s.running {
		if sq < seq && !cs.Done {
			return false
		}
	}
	return true
}

// KillForeground terminates the foreground command's process group directly
// (SIGTERM then SIGKILL), which is the deterministic way to clear a runaway
// command without risking the shell. pgid must be a real foreground group
// distinct from the shell's own group.
func (s *Session) KillForeground(pgid int) {
	if pgid <= 1 {
		return
	}
	bashPgrp := 0
	if pid := s.PID(); pid > 0 {
		if st, err := readProcessState(pid); err == nil {
			bashPgrp = st.Pgrp
		}
	}
	if pgid == bashPgrp || pgid == syscall.Getpgrp() {
		return // never signal the shell's or our own group
	}
	syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(300 * time.Millisecond)
	syscall.Kill(-pgid, syscall.SIGKILL)
}

// Restart rebuilds the shell in place after it has died, preserving the SID,
// directory, and sequence counter so the session stays addressable. Shell
// state (cwd, env, shell vars) is necessarily lost — the shell exited — and the
// cwd resets to HOME. Returns an error if a fresh shell cannot be started.
func (s *Session) Restart() error {
	// Single-flight: a killed session never restarts, and concurrent death
	// detections collapse to one rebuild.
	s.mu.Lock()
	if s.killed {
		s.mu.Unlock()
		return fmt.Errorf("session killed")
	}
	if s.restarting {
		s.mu.Unlock()
		return nil
	}
	s.restarting = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.restarting = false
		s.mu.Unlock()
	}()

	shell, ptmx, err := startShell(s.SID)
	if err != nil {
		return err
	}

	s.ptyMu.Lock()
	oldPty := s.pty
	s.pty = ptmx
	s.ptyMu.Unlock()
	if oldPty != nil {
		oldPty.Close() // retires the previous readPTY goroutine
	}

	s.mu.Lock()
	oldShell := s.shell
	s.shell = shell
	s.running = make(map[int]*CmdState)
	s.cwd = os.Getenv("HOME")
	if !s.killed {
		s.dead = false
	}
	s.mu.Unlock()

	if oldShell != nil && oldShell.Process != nil {
		oldShell.Process.Kill()
		go oldShell.Wait() // reap
	}

	s.writePIDFile()
	go s.readPTY(ptmx)

	if err := s.injectWrapper(); err != nil {
		s.MarkDead()
		return fmt.Errorf("restart wrapper injection: %w", err)
	}
	return nil
}

// recoverShell clears a stuck foreground command and re-establishes the wrapper.
// It NEVER sends a bare `exit` to the top-level shell (that would destroy the
// session); the daemon kills runaway process groups directly via KillForeground.
// If the shell is unresponsive, it rebuilds it via Restart.
func (s *Session) recoverShell() {
	if !s.shellRunning() {
		s.Restart()
		return
	}
	// Interrupt anything in the foreground and clear the input line.
	s.writePTY([]byte{0x03}) // Ctrl-C
	s.writePTY([]byte{0x15}) // Ctrl-U (kill line)
	s.writePTY([]byte{'\n'})
	time.Sleep(150 * time.Millisecond)

	// Re-source the wrapper and confirm responsiveness. If the shell won't
	// acknowledge, it is wedged or gone — rebuild it.
	if err := s.injectWrapper(); err != nil {
		s.Restart()
	}
}

// Kill terminates the session permanently (it will not auto-restart).
func (s *Session) Kill() error {
	s.mu.Lock()
	s.killed = true
	s.dead = true
	shell := s.shell
	s.mu.Unlock()

	if shell != nil && shell.Process != nil {
		shell.Process.Kill()
		go shell.Wait() // reap to avoid a lingering zombie
	}
	s.ptyMu.Lock()
	if s.pty != nil {
		s.pty.Close()
		s.pty = nil
	}
	s.ptyMu.Unlock()
	return nil
}

// CountLines returns the number of lines in a file.
func CountLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	if len(data) == 0 {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		return len(lines) - 1
	}
	return len(lines)
}
