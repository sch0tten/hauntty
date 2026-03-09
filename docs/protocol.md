# Wire Protocol

Line-delimited JSON over unix socket. All session-targeted operations accept a `sid` field (empty = primary session).

## Command Lifecycle

```
Client                          Daemon
  │                                │
  ├─ exec {cmd, sid} ────────────►│
  │◄──────────────── ack {seq} ───┤
  │                                │  ┌─ monitorCommand goroutine
  │◄──────────── status {state} ──┤  │  samples /proc every 1s
  │◄──────────── status {cpu,io} ─┤  │  sends status every 3s
  │◄──────────── prompt {msg} ────┤  │  if waiting_input detected
  │◄──────────── done {rc,cwd} ───┤  └─ rc file appears
  │                                │
  ├─ read {seq, stream} ─────────►│
  │◄──────────── data {lines} ────┤
```

## Operations

| Operation | Direction | Purpose |
|-----------|-----------|---------|
| `exec` | client → daemon | Execute command in session |
| `ack` | daemon → client | Confirm exec, return seq number |
| `status` | daemon → client | Live status (state, CPU, I/O, elapsed) |
| `prompt` | daemon → client | Command waiting for input |
| `done` | daemon → client | Command completed (rc, CWD, line counts) |
| `read` | client → daemon | Read stdout/stderr with offset+limit |
| `data` | daemon → client | Output lines |
| `peek` | client → daemon | Last N PTY lines |
| `screen` | daemon → client | PTY lines (ANSI-stripped) |
| `poll` | client → daemon | Check if command completed |
| `watch` | client → daemon | Spectate live PTY stream |
| `spawn` | client → daemon | Create new parallel session |
| `list` | client → daemon | List all sessions |
| `kill` | client → daemon | Kill a session |
| `input` | client → daemon | Send text to PTY stdin |

## Process States

| State | Meaning | Detection |
|-------|---------|-----------|
| `running` | CPU or I/O active | `/proc/<pid>/stat` state + CPU delta |
| `waiting_input` | Sleeping on terminal read | `/proc/<pid>/wchan` = `n_tty_read` |
| `io_wait` | Kernel disk I/O | `/proc/<pid>/stat` state = `D` |
| `idle` | Sleeping, not on tty | Default sleeping state |
| `done` | Command completed | rc file exists |
