package protocol

// Request/response messages for the hauntty wire protocol.
// Line-delimited JSON over unix socket.

// Operations
const (
	OpExec     = "exec"
	OpAck      = "ack"
	OpDone     = "done"
	OpRead     = "read"
	OpData     = "data"
	OpPeek     = "peek"
	OpScreen   = "screen"
	OpWatch    = "watch"
	OpPoll     = "poll"
	OpStatus   = "status"
	OpList     = "list"
	OpSessions = "sessions"
	OpKill     = "kill"
	OpOK       = "ok"
	OpError    = "error"
	OpPrompt   = "prompt" // command is waiting for user input
	OpInput    = "input"  // client sends text to PTY stdin
	OpSpawn    = "spawn"  // create a new session within this daemon
)

// Command states (used in OpStatus responses)
const (
	StateRunning      = "running"       // CPU or I/O active
	StateWaitingInput = "waiting_input" // process sleeping on tty read
	StateIOWait       = "io_wait"       // kernel disk I/O wait
	StateIdle         = "idle"          // sleeping, not on tty
	StateDone         = "done"          // command completed
)

// Request is the unified request envelope sent from client to daemon.
type Request struct {
	Op             string `json:"op"`
	Cmd            string `json:"cmd,omitempty"`             // exec
	Seq            int    `json:"seq,omitempty"`             // exec (optional — daemon assigns if 0), poll, read
	Stream         string `json:"stream,omitempty"`          // read: "stdout" or "stderr"
	Offset         int    `json:"offset,omitempty"`          // read: line offset
	Limit          int    `json:"limit,omitempty"`           // read: max lines
	Lines          int    `json:"lines,omitempty"`           // peek: number of lines
	SID            string `json:"sid,omitempty"`             // target session (empty = primary); used by exec, read, peek, poll, input, watch, kill
	NonInteractive bool   `json:"non_interactive,omitempty"` // exec: auto-answer prompts with yes
	Input          string `json:"input,omitempty"`           // input: text to send to PTY
}

// Response is the unified response envelope sent from daemon to client.
type Response struct {
	Op          string        `json:"op"`
	SID         string        `json:"sid,omitempty"` // returned by spawn, included in session-targeted responses
	Seq         int           `json:"seq,omitempty"`
	RC          *int          `json:"rc,omitempty"` // pointer to distinguish 0 from absent
	StdoutLines int           `json:"stdout_lines,omitempty"`
	StderrLines int           `json:"stderr_lines,omitempty"`
	CWD         string        `json:"cwd,omitempty"`
	State       string        `json:"state,omitempty"`   // status: classification
	Stream      string        `json:"stream,omitempty"`  // data
	DataLines   []string      `json:"lines,omitempty"`   // data, screen
	Sessions    []SessionInfo `json:"sessions,omitempty"`
	Error       string        `json:"error,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`  // detected interactive prompt text
	CPU         float64       `json:"cpu,omitempty"`     // CPU percentage (status)
	IOBytes     int64         `json:"io_bytes,omitempty"` // total I/O bytes (status)
	Elapsed     float64       `json:"elapsed_s,omitempty"` // seconds since exec (status)
	ChildPID    int           `json:"child_pid,omitempty"` // monitored child PID (status)
}

// SessionInfo describes an active session.
type SessionInfo struct {
	SID     string `json:"sid"`
	User    string `json:"user,omitempty"`
	Host    string `json:"host,omitempty"`
	IP      string `json:"ip,omitempty"`
	PID     int    `json:"pid"`
	Created string `json:"created"`
	CWD     string `json:"cwd"`
	LastSeq int    `json:"last_seq"`
	Primary bool   `json:"primary,omitempty"`
	Alive   bool   `json:"alive"`
}
