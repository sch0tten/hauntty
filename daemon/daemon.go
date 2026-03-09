package daemon

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sch0tten/hauntty/protocol"
)

// DefaultBaseDir returns ~/.hauntty (user-writable, no root needed).
// Falls back to /tmp/hauntty if HOME is unset.
func DefaultBaseDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".hauntty")
	}
	return "/tmp/hauntty"
}

// MaxSessions is the maximum number of parallel sessions per daemon.
const MaxSessions = 16

// Daemon manages sessions and listens on a unix socket.
type Daemon struct {
	BaseDir    string
	controller *Controller
	listener   net.Listener
	primarySID string // SID of the first session, used as default
}

// New creates a new daemon.
func New(baseDir string) *Daemon {
	if baseDir == "" {
		baseDir = DefaultBaseDir()
	}
	return &Daemon{
		BaseDir:    baseDir,
		controller: NewController(baseDir),
	}
}

// Start initializes the daemon: creates a session and starts listening.
func (d *Daemon) Start() error {
	if err := os.MkdirAll(d.BaseDir, 0750); err != nil {
		return fmt.Errorf("create base dir: %w", err)
	}

	// Create initial session
	sess, err := NewSession(d.BaseDir)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	d.primarySID = sess.SID
	d.controller.PrimarySID = sess.SID
	d.controller.AddSession(sess)

	log.Printf("session created: %s (dir: %s)", sess.SID, sess.Dir)

	// Start unix socket listener
	sockPath := filepath.Join(sess.Dir, "hauntty.sock")
	// Also create a well-known symlink
	globalSock := fmt.Sprintf("/tmp/hauntty-%s.sock", sess.SID)

	os.Remove(sockPath)
	os.Remove(globalSock)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		sess.Kill()
		return fmt.Errorf("listen unix: %w", err)
	}
	d.listener = ln

	// Symlink for convenience
	os.Symlink(sockPath, globalSock)

	log.Printf("listening on %s", sockPath)
	log.Printf("session ID: %s", sess.SID)
	fmt.Printf("HAUNTTY_SID=%s\n", sess.SID)
	fmt.Printf("HAUNTTY_SOCK=%s\n", globalSock)

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		d.Shutdown()
		os.Exit(0)
	}()

	// Accept connections
	for {
		conn, err := ln.Accept()
		if err != nil {
			if d.listener == nil {
				return nil // shutdown
			}
			log.Printf("accept error: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
}

// resolveSession finds the session for a request. If SID is empty, uses the primary session.
func (d *Daemon) resolveSession(sid string) (*Session, error) {
	if sid == "" {
		sid = d.primarySID
	}
	sess, ok := d.controller.GetSession(sid)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sid)
	}
	return sess, nil
}

func (d *Daemon) handleConn(conn net.Conn) {
	defer conn.Close()

	dec := protocol.NewDecoder(conn)
	enc := protocol.NewEncoder(conn)

	for {
		var req protocol.Request
		if err := dec.Decode(&req); err != nil {
			if err != io.EOF {
				log.Printf("decode error: %v", err)
			}
			return
		}

		switch req.Op {
		case protocol.OpSpawn:
			d.handleSpawn(enc)

		case protocol.OpExec:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			d.handleExec(enc, sess, &req)

		case protocol.OpPoll:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			d.handlePoll(enc, sess, &req)

		case protocol.OpRead:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			d.handleRead(enc, sess, &req)

		case protocol.OpPeek:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			d.handlePeek(enc, sess, &req)

		case protocol.OpWatch:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			sess.AddWatcher(conn)
			buf := make([]byte, 1)
			conn.Read(buf) // blocks until EOF
			return

		case protocol.OpList:
			d.handleList(enc)

		case protocol.OpKill:
			d.handleKill(enc, &req)

		case protocol.OpInput:
			sess, err := d.resolveSession(req.SID)
			if err != nil {
				enc.Encode(&protocol.Response{Op: protocol.OpError, Error: err.Error()})
				continue
			}
			d.handleInput(enc, sess, &req)

		default:
			enc.Encode(&protocol.Response{
				Op:    protocol.OpError,
				Error: fmt.Sprintf("unknown op: %s", req.Op),
			})
		}
	}
}

func (d *Daemon) handleSpawn(enc *protocol.Encoder) {
	count := d.controller.SessionCount()
	if count >= MaxSessions {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("session limit reached (%d/%d)", count, MaxSessions),
		})
		return
	}

	sess, err := NewSession(d.BaseDir)
	if err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("spawn session: %v", err),
		})
		return
	}

	d.controller.AddSession(sess)
	log.Printf("spawned session: %s (dir: %s)", sess.SID, sess.Dir)

	enc.Encode(&protocol.Response{
		Op:  protocol.OpOK,
		SID: sess.SID,
	})
}

func (d *Daemon) handleExec(enc *protocol.Encoder, sess *Session, req *protocol.Request) {
	seq := req.Seq
	if seq == 0 {
		seq = sess.NextSeq()
	} else {
		// Ensure seq tracking is updated
		sess.mu.Lock()
		if seq > sess.seq {
			sess.seq = seq
		}
		sess.mu.Unlock()
	}

	if err := sess.Exec(seq, req.Cmd); err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("exec: %v", err),
		})
		return
	}

	enc.Encode(&protocol.Response{
		Op:  protocol.OpAck,
		Seq: seq,
	})

	// Delegate monitoring to the controller
	d.controller.ExecCommand(sess, seq, req.Cmd, enc, req.NonInteractive)
}

func (d *Daemon) handleInput(enc *protocol.Encoder, sess *Session, req *protocol.Request) {
	if req.Input == "" {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: "input: empty input",
		})
		return
	}

	if err := d.controller.SendInput(sess, req.Input); err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("input: %v", err),
		})
		return
	}

	enc.Encode(&protocol.Response{Op: protocol.OpOK})
}

func (d *Daemon) handlePoll(enc *protocol.Encoder, sess *Session, req *protocol.Request) {
	done, rc, err := sess.Poll(req.Seq)
	if err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("poll: %v", err),
		})
		return
	}

	if done {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpStatus,
			Seq:   req.Seq,
			State: protocol.StateDone,
			RC:    &rc,
		})
	} else {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpStatus,
			Seq:   req.Seq,
			State: protocol.StateRunning,
		})
	}
}

func (d *Daemon) handleRead(enc *protocol.Encoder, sess *Session, req *protocol.Request) {
	stream := req.Stream
	if stream == "" {
		stream = "stdout"
	}

	lines, err := sess.ReadOutput(req.Seq, stream, req.Offset, req.Limit)
	if err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: fmt.Sprintf("read: %v", err),
		})
		return
	}

	enc.Encode(&protocol.Response{
		Op:        protocol.OpData,
		Seq:       req.Seq,
		Stream:    stream,
		DataLines: lines,
	})
}

func (d *Daemon) handlePeek(enc *protocol.Encoder, sess *Session, req *protocol.Request) {
	n := req.Lines
	if n <= 0 {
		n = 20
	}

	lines := sess.Peek(n)
	enc.Encode(&protocol.Response{
		Op:        protocol.OpScreen,
		DataLines: lines,
	})
}

func (d *Daemon) handleList(enc *protocol.Encoder) {
	enc.Encode(&protocol.Response{
		Op:       protocol.OpSessions,
		Sessions: d.controller.ListSessions(),
	})
}

func (d *Daemon) handleKill(enc *protocol.Encoder, req *protocol.Request) {
	sid := req.SID
	if sid == "" {
		enc.Encode(&protocol.Response{Op: protocol.OpError, Error: "kill requires a session ID"})
		return
	}

	if err := d.controller.RemoveSession(sid); err != nil {
		enc.Encode(&protocol.Response{
			Op:    protocol.OpError,
			Error: err.Error(),
		})
		return
	}

	enc.Encode(&protocol.Response{Op: protocol.OpOK})

	// If primary session was killed, shut down the daemon
	if sid == d.primarySID {
		log.Printf("primary session killed, shutting down daemon")
		go func() {
			time.Sleep(100 * time.Millisecond)
			d.Shutdown()
			os.Exit(0)
		}()
	}
}

// Shutdown cleanly stops all sessions and the listener.
func (d *Daemon) Shutdown() {
	if d.listener != nil {
		d.listener.Close()
		d.listener = nil
	}

	d.controller.Shutdown()
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
