# hauntty — TODO

## Fixed in the durability pass (2026-05-31)

These were reproduced (on a remote host + a local `-race` daemon) and fixed with regression tests. See `daemon/*_test.go`.

- [x] **Premature completion gate on background processes** — `cmd &` reported done while the bg process kept running. Now the foreground rc is still the gate, but lingering `&` jobs are reported in `background_pids`, and `hauntty wait --pid N` blocks on them deterministically (local /proc, no `sleep; ssh ps` loop).
- [x] **Background/leftover children corrupting monitoring** — the monitor watched ALL bash children, so `&` jobs and prior-command leftovers reported the wrong PID and masked `read -p` prompt detection. Now it tracks the terminal **foreground process group** (`tpgid`) only.
- [x] **Dead session lied about being alive** — after `exit N`, bash was a zombie but `IsAlive()` (signal 0) said alive and the session silently ate commands. Now `/proc` state is read (Z = dead), the session **auto-restarts** under the same SID, and the caller is told the shell was reset.
- [x] **`recoverShell` destroyed live sessions** — it sent a blind `exit`, killing a non-nested shell (a recovery storm under load). Now runaway commands are killed by **process group**; `recoverShell` only sends Ctrl-C and re-verifies, never a bare `exit`; a dead shell is rebuilt via `Restart`.
- [x] **Configurable command timeout** — `--timeout` is plumbed per-exec through the protocol; a runaway is killed by its foreground group (real rc preserved, e.g. 143). The client waits past the daemon deadline so it always receives the verdict.
- [x] **Wrapper readiness probe** — the 100ms sleep is gone; injection writes a nonce and the daemon polls for it (works under load), so the first command never hits "command not found".
- [x] **CWD capture race + PTY pollution** — the wrapper now writes `cwd` (before `rc`); no `pwd > .cwd` PTY write, no 50ms sleep, no peek noise.
- [x] **Connection write races** — `protocol.Encoder` is mutex-guarded; `Session.seq`/`cwd`/`dead` use locked accessors. Verified with `go test -race`.
- [x] **CPU% underflow** — per-PID jiffy deltas instead of subtracting totals (no uint64 wrap when the child set changes).
- [x] **Stale socket on shutdown** — the daemon removes its socket + `/tmp` symlink on shutdown.
- [x] **Slow watcher could stall a session** — spectator writes are now non-blocking (short deadline, dropped on stall).
- [x] **Spawn session-cap race** — the limit is enforced atomically.
- [x] Command file exposure — `cmd.N.pending` is `0600` and removed by the wrapper after use; socket is `0600`.
- [x] Peek output filtering — wrapper bookkeeping lines are hidden from `peek` (attach still shows raw).

## Still open

### Security
- [ ] **Socket authentication** — `chmod 0600` is done; a nonce/token handshake on connect is not. Evaluate moving the socket out of `/tmp`.
- [ ] **Blind prompt confirmation** — `--yes` still answers any detected prompt. Plan: a whitelist of acceptable prompts, or a round-trip back to the agent.

### Gate / observability
- [ ] **`hauntty wait` predicates** — beyond `--pid`/`--seq`: `--port` (listening), `--path` (exists), `--grep` (log line). Each replaces a `sleep; ssh check` loop.
- [ ] Wait on a process **group** (a bg job that forks children), not just a single pid.

### Backlog
- [ ] **Syslog forwarding** — optional `--syslog <address>`, structured RFC 5424 per command completion for SIEM/live-parsing.
