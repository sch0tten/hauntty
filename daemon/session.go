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

// Session represents a persistent shell session with a PTY.
type Session struct {
	SID      string
	User     string
	Hostname string
	IP       string
	Created  time.Time
	Dir      string // ~/.hauntty/<sid>
	Shell    *exec.Cmd
	PTY      *os.File
	CWD      string

	mu       sync.Mutex
	seq      int
	running  map[int]*CmdState // seq -> state
	ptyBuf   *RingBuffer       // circular buffer of recent PTY output
	watchers []io.Writer       // spectators receiving raw PTY bytes
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
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewSession creates a new persistent shell session.
func NewSession(baseDir string) (*Session, error) {
	sid := generateSID()
	dir := filepath.Join(baseDir, sid)

	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	shell := exec.Command("/bin/bash", "--norc", "--noprofile")
	shell.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PS1=$ ",
		fmt.Sprintf("HAUNTTY_SID=%s", sid),
	)

	ptmx, err := pty.Start(shell)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("start pty: %w", err)
	}

	// Set initial terminal size
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 200})

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
		Shell:    shell,
		PTY:      ptmx,
		CWD:      os.Getenv("HOME"),
		running:  make(map[int]*CmdState),
		ptyBuf:   NewRingBuffer(1000),
	}

	// Write PID file
	pidFile := filepath.Join(dir, "hauntty.pid")
	os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", shell.Process.Pid)), 0644)

	// Initialize session log
	logFile := filepath.Join(dir, "session.log")
	os.WriteFile(logFile, []byte(""), 0644)

	// Start PTY reader goroutine
	go s.readPTY()

	// Inject the exec wrapper after shell starts
	time.Sleep(100 * time.Millisecond) // let bash initialize
	s.injectWrapper()

	return s, nil
}

// injectWrapper writes the __hauntty_exec function into the shell.
// Uses a heredoc + source approach to avoid PTY echo noise.
func (s *Session) injectWrapper() {
	wrapperPath := filepath.Join(s.Dir, ".hauntty_wrapper.sh")
	wrapper := fmt.Sprintf(`__hauntty_dir="%s"

__hauntty_exec() {
    local seq="$1"
    local cmd_dir="${__hauntty_dir}/cmd.${seq}"
    local pending="${__hauntty_dir}/cmd.${seq}.pending"
    local cmd
    cmd="$(cat "$pending")"
    mkdir -p "${cmd_dir}"
    cp "$pending" "${cmd_dir}/cmdline"
    echo "### CMD ${seq} [$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] ${cmd} ###"
    # Save original fds, redirect stdout/stderr to files
    exec 3>&1 4>&2
    exec 1>"${cmd_dir}/stdout" 2>"${cmd_dir}/stderr"
    eval "$cmd"
    local rc=$?
    # Restore original fds
    exec 1>&3 2>&4
    exec 3>&- 4>&-
    # Echo captured output to PTY for spectators/peek
    cat "${cmd_dir}/stdout"
    if [ -s "${cmd_dir}/stderr" ]; then
        cat "${cmd_dir}/stderr" >&2
    fi
    echo "$rc" > "${cmd_dir}/rc"
    echo "### END ${seq} RC=${rc} [$(date '+%%Y-%%m-%%d %%H:%%M:%%S')] ###"
    return $rc
}
`, s.Dir)

	// Write wrapper to file, then source it — avoids multi-line PTY echo
	os.WriteFile(wrapperPath, []byte(wrapper), 0644)
	s.PTY.Write([]byte(fmt.Sprintf("source %s\n", wrapperPath)))
}

// readPTY continuously reads from the PTY and distributes output.
func (s *Session) readPTY() {
	buf := make([]byte, 4096)
	var lineBuf strings.Builder

	for {
		n, err := s.PTY.Read(buf)
		if err != nil {
			return
		}
		chunk := buf[:n]

		// Distribute to spectators
		s.watchMu.Lock()
		alive := s.watchers[:0]
		for _, w := range s.watchers {
			if _, werr := w.Write(chunk); werr == nil {
				alive = append(alive, w)
			}
		}
		s.watchers = alive
		s.watchMu.Unlock()

		// Buffer lines for peek (strip ANSI escape codes)
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

// NextSeq returns the next command sequence number.
func (s *Session) NextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Exec sends a command to the shell via the exec wrapper.
func (s *Session) Exec(seq int, cmd string) error {
	s.mu.Lock()
	s.running[seq] = &CmdState{
		Seq:       seq,
		Cmd:       cmd,
		StartTime: time.Now().UTC(),
	}
	s.mu.Unlock()

	// Write command to pending file — the wrapper reads it directly
	cmdFile := filepath.Join(s.Dir, fmt.Sprintf("cmd.%d.pending", seq))
	if err := os.WriteFile(cmdFile, []byte(cmd), 0644); err != nil {
		return fmt.Errorf("write cmd file: %w", err)
	}
	line := fmt.Sprintf("__hauntty_exec %d\n", seq)
	_, err := s.PTY.Write([]byte(line))
	return err
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

	// Mark as done
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
	// Remove trailing empty line from split
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

// Peek returns the last N lines of PTY output.
func (s *Session) Peek(n int) []string {
	return s.ptyBuf.LastN(n)
}

// AddWatcher adds a spectator writer.
func (s *Session) AddWatcher(w io.Writer) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.watchers = append(s.watchers, w)
}

// GetCWD reads the current working directory from the shell.
func (s *Session) GetCWD() string {
	// We'll update CWD after each command by reading it
	return s.CWD
}

// UpdateCWD asks the shell for its current directory.
func (s *Session) UpdateCWD() {
	// Write pwd to a temp file and read it back
	cwdFile := filepath.Join(s.Dir, ".cwd")
	cmd := fmt.Sprintf("pwd > %s\n", cwdFile)
	s.PTY.Write([]byte(cmd))
	time.Sleep(50 * time.Millisecond)
	if data, err := os.ReadFile(cwdFile); err == nil {
		s.CWD = strings.TrimSpace(string(data))
	}
}

// recoverShell attempts to break out of a stuck state (nested shell, hung process)
// by sending Ctrl-C and exit commands, then re-injecting the wrapper function.
func (s *Session) recoverShell() {
	s.PTY.Write([]byte("\x03\n"))   // Ctrl-C to interrupt anything running
	time.Sleep(200 * time.Millisecond)
	s.PTY.Write([]byte("exit\n"))   // exit nested shell if any
	time.Sleep(200 * time.Millisecond)
	s.PTY.Write([]byte("exit\n"))   // in case of double nesting
	time.Sleep(300 * time.Millisecond)
	s.injectWrapper()
	time.Sleep(200 * time.Millisecond)
}

// IsAlive checks if the session's bash process is still running.
func (s *Session) IsAlive() bool {
	if s.Shell.Process == nil {
		return false
	}
	// Signal 0 checks if process exists without actually sending a signal
	err := s.Shell.Process.Signal(syscall.Signal(0))
	return err == nil
}

// Kill terminates the session.
func (s *Session) Kill() error {
	if s.Shell.Process != nil {
		s.Shell.Process.Kill()
	}
	s.PTY.Close()
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
