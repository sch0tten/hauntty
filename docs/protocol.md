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
  │◄─ done {rc,cwd,background_pids}┤  └─ rc file appears
  │                                │
  ├─ read {seq, stream} ─────────►│
  │◄──────────── data {lines} ────┤
```

`exec` accepts `timeout_s` (per-command hard deadline; the daemon kills a
runaway by its foreground process group and the wrapper records the real rc).
`done` and `status` carry `background_pids` — the PIDs of any `&` jobs the
command left running. Gate on them with `wait`.

## Gate Lifecycle

```
Client                          Daemon
  ├─ wait {pid} ────────────────►│  poll /proc/<pid> locally
  │◄──────────── done {state} ───┤  state="exited" when gone
  │                               │
  ├─ wait {seq} ────────────────►│  poll the command's rc file
  │◄────── done {seq, rc} ───────┤
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
| `wait` | client → daemon | Block until a `pid` exits or a `seq` completes |
| `list` | client → daemon | List all sessions |
| `kill` | client → daemon | Kill a session |
| `input` | client → daemon | Send text to PTY stdin |

## Process States

Command state is derived from the **terminal foreground process group**
(`tpgid` in `/proc/<bash>/stat`) — the processes bash is currently running in
the foreground — not from enumerating all of bash's children. Background jobs
and leftover children therefore never alter the current command's state.

| State | Meaning | Detection |
|-------|---------|-----------|
| `running` | CPU or I/O active | foreground group has a process in state `R` (or CPU delta) |
| `waiting_input` | Sleeping on terminal read | foreground (or bash, for a `read` builtin) on a tty wait channel |
| `io_wait` | Kernel disk I/O | foreground group has a process in state `D` |
| `idle` | Sleeping, not on tty | foreground sleeping, not on a tty wait |
| `done` | Command completed | rc file exists |
| `zombie` | Shell exited | `/proc/<bash>` state `Z` or `tpgid == -1` (triggers self-heal) |
