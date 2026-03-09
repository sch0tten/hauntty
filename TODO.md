# hauntty — TODO

## Known Issues — Working for Next Release

### Security

- [ ] **Socket authentication** — the Unix socket (`/tmp/hauntty-<sid>.sock`) has no authentication. Any process on the remote host that can reach it can execute arbitrary commands as the daemon's user. Plan: `chmod 0600` the socket, implement a nonce/token handshake on connect (random 32-byte token written to `~/.hauntty/<sid>/token`, required in the first message), evaluate moving the socket into the session directory exclusively and dropping the `/tmp` symlink.
- [ ] **Command file exposure** — pending command files (`cmd.N.pending`) are written to disk as plaintext before execution. Commands containing secrets (API keys, database passwords) are persisted. Plan: set `0600` permissions, post-exec cleanup, evaluate in-memory passing.
- [ ] **Blind prompt confirmation** — `--yes` mode sends `"yes\n"` to any detected prompt without discrimination. In production, this could confirm destructive operations the agent didn't intend to approve. Plan: whitelist of acceptable prompt patterns, or a confirmation round-trip back to the agent.

### Hard Deadline

- [ ] **Configurable command timeout** — `hardDeadline` is fixed at 10 minutes. For MLOps workloads (large builds, docker builds, database migrations, model downloads, filesystem scans), 10 minutes is insufficient. The `--timeout` flag exists on the CLI but isn't plumbed through to the daemon's hard deadline. The daemon silently kills long-running operations and recovers the shell, which is confusing to debug. Plan: pass timeout per-exec through the wire protocol.

### Shell Wrapper Injection

- [ ] **Wrapper readiness probe** — `time.Sleep(100ms)` after PTY start before injecting the wrapper is a race condition. On a loaded host, bash may not be ready in 100ms. `injectWrapper()` has no verification that the wrapper was actually sourced — if it fails silently, every subsequent `__hauntty_exec` call fails with "command not found" (detected by the controller, but only after 3-5 seconds). Plan: write a sentinel value via the wrapper and poll for its existence.

## Backlog

### Syslog Forwarding
- [ ] Optional `--syslog <address>` on daemon start, emit structured syslog (RFC 5424) per command completion for SIEM/live-parsing

### Cleanup & Resilience
- [x] SSH tunnel cleanup on `hauntty kill`
- [x] Auto-prune dead sessions on `hauntty list`
- [ ] Peek output filtering — hide wrapper invocations from peek output
