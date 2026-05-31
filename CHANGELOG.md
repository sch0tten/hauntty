# Changelog

All notable changes to hauntty are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-05-31

Durability hardening. This release reworks process monitoring, session
lifecycle, and concurrency so hauntty behaves predictably under real
multi-session load, and adds a deterministic completion gate so agents stop
polling with `sleep` + `ssh … ps aux | grep`.

The wire protocol changes are additive and backward compatible (new fields are
omitted when empty). A new-version client talking to an old daemon will get an
"unknown op" error only for the new `wait` operation.

### Added
- **`hauntty wait`** — block until a process exits (`--pid`) or a command
  completes (`--seq`). The daemon waits locally against `/proc` on the remote
  host, replacing the `sleep N; ssh host "ps aux | grep proc"` polling pattern
  with a single deterministic call.
- **Background-process surfacing** — when a command leaves `&` jobs running,
  their PIDs are reported as `background_pids` on the `done` message and in
  live status, so the caller gets a real handle instead of a premature "done".
- **Self-healing sessions** — if a session's shell dies (e.g. the command ran
  `exit`), the daemon rebuilds a fresh PTY + bash under the same SID and tells
  the caller the shell was reset (cwd/env lost) instead of silently failing.
- **Per-exec hard deadline** — `exec -t <dur>` is now plumbed through the wire
  protocol to the daemon (previously the CLI flag only bounded the client).
- Peek output now filters internal wrapper bookkeeping (`### CMD/END`, the
  `source` line); `attach` still streams raw PTY for full fidelity.

### Changed
- **Process monitoring tracks the terminal foreground process group** (`tpgid`)
  instead of enumerating all of bash's children. Classification, CPU%, I/O, and
  the reported PID now reflect the command actually running in the foreground —
  immune to background jobs and leftover children from prior commands.
- Runaway commands at the hard deadline are terminated by their **foreground
  process group** (SIGTERM → SIGKILL); the wrapper still records the real exit
  code (e.g. `143` for SIGTERM) rather than a synthetic `-1`.
- Wrapper injection is **verified** via a readiness nonce before the first
  command runs (replacing a fixed 100 ms sleep that raced on loaded hosts).
- The command's working directory is captured by the wrapper (written before
  the `rc` file) instead of by writing `pwd` to the PTY — no race, no peek
  pollution, no fixed sleep.
- Concurrent commands on the same session are monitored in order: a queued
  command's monitor waits until it is the oldest running command before
  sampling, so it never observes another command's foreground group.
- Spectator (`attach`) writes are non-blocking, so a slow/stalled spectator can
  no longer back-pressure and freeze the session's PTY.

### Fixed
- Background processes (`cmd &`) no longer cause a premature/incorrect "done"
  with the wrong child PID.
- `read -p` and other stdin-reading commands are now detected as prompts
  instead of hanging as `idle` until timeout (a lingering background child used
  to mask detection).
- A dead session (zombie shell after `exit`) is no longer reported `[alive]`
  while silently swallowing commands; liveness is read from `/proc` state.
- Shell recovery no longer sends a blind `exit` that could kill a healthy,
  non-nested session (the "recovery storm" under load).
- CPU% no longer overflows when the set of monitored processes changes between
  samples (now a sum of per-PID jiffy deltas instead of subtracting totals).
- The daemon removes its socket and `/tmp` symlink on shutdown.
- Spawn enforces the session cap atomically (no check-then-add race).

### Security
- The daemon socket and per-command (`cmd.N.pending`) files are created `0600`.
- Pending command files are removed immediately after the wrapper consumes
  them, shrinking the window in which command text (possibly containing
  secrets) is persisted on disk.
- Socket authentication (a nonce/token handshake) is still **not** implemented —
  see [SECURITY.md](SECURITY.md). Continue to treat the daemon's UID as trusted.

### Internal
- Added race-tested integration coverage driving real bash on a PTY:
  `daemon/{procmon,integration,concurrency,wait}_test.go` (21 tests, all run
  under `go test -race`). Added GitHub Actions CI (`go vet`, `go test -race`,
  cross-compile build).

## [0.1.0] - 2026-03-09

Initial public release.

### Added
- Self-deploying single static Go binary: `hauntty connect user@host` SCPs the
  binary, starts the daemon, and forwards the Unix socket over SSH.
- Persistent, observable shell sessions on remote hosts (PTY + bash) with their
  own CWD, environment, and `session.log`.
- Parallel sessions within one daemon (`spawn`), addressed by SID and `--target`
  (up to 16 per daemon).
- Deterministic command state via `/proc` (`stat`, `wchan`, `io`): `running`,
  `waiting_input`, `io_wait`, `idle`, `done`.
- Line-delimited JSON wire protocol over the Unix socket.
- Commands: `exec`, `read`, `peek`, `attach`, `poll`, `list`, `kill`, `spawn`,
  `clean`, `uninstall`, `corpus`, `version`.
- Centralized command corpus log (`corpus.jsonl`) with filtering.
- Shell-completion auto-install, SSH keepalive, version stamping.

[Unreleased]: https://github.com/sch0tten/hauntty/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/sch0tten/hauntty/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/sch0tten/hauntty/releases/tag/v0.1.0
