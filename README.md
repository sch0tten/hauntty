# hauntty

**Stateful, observable shell sessions for LLM agents.**

```
$ hauntty connect myserver
deploying hauntty to myserver...
starting hauntty daemon on myserver...
HAUNTTY_SID=38b25f55
HAUNTTY_SOCK=/tmp/hauntty-38b25f55.sock
HAUNTTY_HOST=myserver

$ hauntty exec -s 38b25f55 "cd /opt/app && git pull && make build"
seq: 1
cmd: cd /opt/app && git pull && make build
rc: 0
stdout_lines: 12
cwd: /opt/app             # CWD persists for the next command

$ hauntty exec -s 38b25f55 "make test"
seq: 2
cmd: make test
rc: 0
stdout_lines: 347         # 347 lines — read only what you need

$ hauntty read -s 38b25f55 --seq 2 --stream stdout --limit 10
```

A single static Go binary that gives LLM agents a real shell on remote hosts: persistent PTY, structured output, deterministic process monitoring via `/proc`, all over one SSH tunnel. Self-deploying — `hauntty connect user@host` and it's running.

## The Problem

When an LLM agent operates on a remote host, every command is a fresh `ssh host "command"`. Each invocation:

- **Opens a new TCP connection** — SSH handshake, key exchange, authentication
- **Starts a disposable shell** — no memory of previous commands, no accumulated state
- **Loses all context** — `cd`, `export`, shell variables, background jobs — gone
- **Dumps raw output blind** — the agent has no idea how large the response is before it lands in its context window
- **Leaves no audit trail** — no record of what ran, when, or what happened

In production MLOps — triaging GPU failures across a fleet, auditing hosts after an incident, verifying rolling deployments — an agent may execute 50-200 commands per session. Each one pays the full SSH handshake cost and dumps unfiltered output into the context window. Over a fleet operation, this means hundreds of kilobytes of noise displacing the agent's working memory, degrading reasoning quality with every command.

## How hauntty Solves This

```
┌─────────────────┐         SSH Tunnel          ┌──────────────────────────┐
│   LLM Agent     │    (forwarded unix socket)   │   Remote Host            │
│                 │                              │                          │
│  hauntty exec ──┼──── /tmp/hauntty-xxx.sock ───┼── hauntty daemon        │
│  hauntty read   │                              │       │                 │
│  hauntty peek   │                              │  ┌────┴────┐            │
│  hauntty spawn  │                              │  │Controller│            │
│  hauntty list   │                              │  └────┬────┘            │
│                 │                              │       │                 │
│                 │                              │  Session A (primary)     │
│                 │                              │    PTY + bash            │
│                 │                              │    cmd.1/ cmd.2/ ...     │
│                 │                              │    session.log           │
│                 │                              │                          │
│                 │                              │  Session B (spawned)     │
│                 │                              │    PTY + bash            │
│                 │                              │    cmd.1/ cmd.2/ ...     │
│                 │                              │    session.log           │
│                 │                              │                          │
│                 │                              │  ... up to 16 sessions   │
└─────────────────┘                              └──────────────────────────┘
```

- **One SSH connection**, tunneled once, reused for all commands
- **Persistent PTY shell** — CWD, environment, and shell state survive across commands
- **Parallel sessions** — spawn independent sessions for concurrent work, each with its own shell state and audit log
- **Structured output** — every command returns metadata (`seq`, `rc`, `stdout_lines`, `stderr_lines`, `cwd`) *before* the payload, so the agent can decide whether to read or skip
- **Per-command capture** — stdout, stderr, return code, and the command text stored in separate files, readable at any time with offset and limit
- **Session log** — YAML audit trail per session: every command, timestamp, return code, output size
- **Corpus log** — centralized JSONL log across all sessions, queryable with `hauntty corpus` and filterable by time, host, session, or failure status
- **Process monitoring via /proc** — deterministic process classification (running, waiting_input, io_wait, idle, done) using kernel state, not regex heuristics
- **Self-deploying** — `hauntty connect user@host` SCPs the binary, starts the daemon, forwards the socket. No installation needed.

## Why hauntty Matters

Benchmark task: "Check if the server is healthy through Linux logs" — dmesg errors, journalctl, GPU issues, OOM events, storage problems. A standard blind discovery pass where the agent doesn't know what it'll find. 8 diagnostic commands per server.

### 1. Connection Overhead

| | Raw SSH | hauntty |
|---|---------|---------|
| Connections per 8-command task | **8** (one per command) | **0** (persistent session) |
| SSH handshakes | 8 full key exchanges | 0 after initial `connect` |
| Shell state preserved | No — fresh shell every time | Yes — CWD, env, history carry over |

Every `ssh host "command"` pays the full SSH handshake: TCP connect, key exchange, authentication, shell spawn. On a production server that's ~250 ms overhead per command on LAN, ~800 ms over the internet — before a single byte of useful work happens.

hauntty pays this cost once. `hauntty connect` establishes one SSH tunnel, and every subsequent command reuses the persistent session. For an 8-command health check, that's 8 eliminated handshakes. For a 50-command incident triage across 20 servers, that's **1,000 eliminated handshakes**.

The shell persistence matters operationally: `cd /opt/app && export ENV=prod` in command 1 is still there in command 40. With raw SSH, every command starts in `$HOME` with a blank environment — the agent wastes commands re-establishing state.

### 2. Wall Clock Efficiency

| | Raw SSH | hauntty | Improvement |
|---|---------|---------|---|
| 8-command health check | 2,388 ms | 1,200 ms | **2x faster** |
| Same task, 50 ms network latency | 6,910 ms | 2,358 ms | **2.9x faster** |

The improvement scales with latency because the overhead is per-connection, not per-byte. hauntty eliminates it entirely.

**At fleet scale, this compounds.** Consider a 10,000-server fleet audit — 8 diagnostic commands per host, 80,000 total commands:

| | Raw SSH | hauntty |
|---|---------|---------|
| SSH handshakes | 80,000 | 10,000 (one per host) |
| Connection overhead alone (~800 ms each) | **17.7 hours** | **2.2 hours** |

hauntty is a single static Go binary with native goroutine concurrency. No event loop contention, no GIL, no asyncio compatibility issues with legacy Python libraries. Each host connection is a goroutine — 10,000 concurrent sessions are a scheduling problem, not an architectural one.

### 3. Token Economy

This is the critical advantage.

**The raw SSH problem:** every `ssh host "command"` returns its complete output directly into the LLM agent's context window. The agent cannot preview, filter, or skip. A single `journalctl -p warning` query returned **4,327 lines (1.1 MB)** in our benchmark — all of it consumed as input tokens before the agent could decide it was noise.

**hauntty inverts the flow.** The agent receives structured metadata first — `stdout_lines: 4327`, `rc: 0`, `elapsed_s: 0.3` — and decides what to read:

| Pattern | Example | What the agent sees |
|---------|---------|---------------------|
| **Count before read** | `grep -c 'Failed'` | `stdout_lines: 1` → just the count, skip the full output |
| **Filter at source** | `journalctl -p warning \| grep -v noise \| tail -20` | 20 lines — the signal, not 4,327 lines of noise |
| **Skip entirely** | `dmesg \| grep -i 'oom'` | `stdout_lines: 0` → check passed, nothing to read |

Measured impact on the same 8-command health check:

| | Raw SSH | hauntty | Reduction |
|---|---------|---------|---|
| **Context consumed** | **1.1 MB** (4,400+ lines dumped blind) | **13 KB** (metadata + 44 targeted lines) | **99%** |
| Tokens billed (estimated at ~4 chars/token) | ~282,000 input tokens | ~3,300 input tokens | **99%** |

**But the real cost isn't the API bill — it's context window drift.**

LLM agents have finite context windows. Every byte of irrelevant output displaces working memory — previous findings, the current plan, tool definitions, the task itself. As context fills with noise, the agent loses coherence: it forgets earlier conclusions, repeats queries, contradicts its own findings, or misses patterns it already identified.

This is **context drift** — the progressive degradation of an agent's reasoning quality as its context window fills with low-signal content. It's the difference between an agent that synthesizes findings across 50 commands into a coherent diagnosis, and one that loses the thread after 15.

In production MLOps — triaging a training failure across 8 GPU nodes, auditing a fleet after a security incident, verifying a rolling deployment — an agent may execute 50-200 commands. At the benchmark average of ~140 KB per raw SSH task, a 50-command session pushes **~900 KB of raw output** into the context window, most of it irrelevant. hauntty's metadata-first design keeps the context clean: the agent reads only the lines that inform its next decision, preserving its capacity to reason over an entire operation instead of drowning in the output of the last three commands.

## Features

### Core
- **Self-bootstrapping** — `hauntty connect user@host` deploys itself to the remote, starts the daemon, and forwards the socket. No pre-installation required.
- **PTY-native** — real pseudo-terminal, not exec-style command running. Interactive programs, terminal colors, and signal handling work correctly.
- **Structured output** — every command returns `seq`, `rc`, `stdout_lines`, `stderr_lines`, `cwd` as metadata. The agent knows what happened before reading the payload.
- **Per-command files** — stdout, stderr, return code, and the command text stored separately in `~/.hauntty/<sid>/cmd.<seq>/`. Read any stream at any time, with offset and limit.
- **Session log** — append-only YAML log per session: timestamp, command text, return code, CWD, output line counts.
- **Corpus log** — centralized JSONL log (`~/.hauntty/corpus.jsonl`) aggregating all commands across sessions with full metadata. Query with `hauntty corpus` and filter by time, host, session, or failure status. Pipe-friendly for `jq`, SIEM ingestion, or analytics.
- **Single static binary** — one Go binary, no dependencies, compiles for linux/amd64 and linux/arm64.

### Parallel Sessions
- **Spawn** — `hauntty spawn -s <sid>` creates a new session within the same daemon. Each session gets its own PTY, bash shell, CWD, environment, seq counter, and `session.log`.
- **Session isolation** — `cd /tmp` in session A does not affect session B. Environment variables, shell functions, aliases — all independent.
- **Parallel execution** — commands serialize within a session (one bash = one command at a time), but run in true parallel across sessions.
- **Session routing** — all ops accept `--target <sid>` to address a specific session. Empty target defaults to the primary session (backward compatible).
- **Up to 16 sessions** per daemon, all sharing one SSH tunnel and one unix socket.
- **Independent lifecycle** — kill a spawned session without affecting others. Kill the primary to shut down the daemon.

### Process Monitoring
- **Deterministic state via /proc** — reads `/proc/<pid>/stat`, `/proc/<pid>/wchan`, `/proc/<pid>/io` to classify commands as `running`, `waiting_input`, `io_wait`, `idle`, or `done`. No regex heuristics.
- **Interactive prompt detection** — detects when a command is waiting for terminal input via kernel wait channel `n_tty_read`. In `--yes` mode, auto-answers prompts.
- **Live status** — long-running commands stream periodic updates with CPU%, I/O bytes, elapsed time, and child PID.
- **Process tree tracking** — monitors child and grandchild processes (pipelines, subshells).

### Observability
- **Peek** — last N lines of raw PTY output (ANSI-stripped). Check what's happening without reading specific command output.
- **Attach** — spectate a session in real time (read-only). Watch what the agent is doing on the remote host.
- **List** — all active sessions across hosts, with user, hostname, IP, CWD, last seq, alive/dead status, and primary marker.
- **Version stamping** — `hauntty version` shows version, git commit, and build date. Deploy decisions based on full version comparison.

### Resilience
- **Shell recovery** — detects wrapper loss, zombie bash, hard timeout. Automatically recovers the shell (Ctrl-C + exit + re-inject wrapper).
- **Pipeline debounce** — brief "done" classifications between pipe stages are debounced (2-tick) to avoid false prompt detection.
- **SSH keepalive** — `ServerAliveInterval=60`, detects dead connections within 3 minutes.
- **Connection diagnostics** — clear error messages when the tunnel or daemon is down, with remediation hints.

## Installation

```bash
# Install directly via Go
go install github.com/sch0tten/hauntty@v0.1.0

# Or build from source with version stamping
git clone https://github.com/sch0tten/hauntty.git
cd hauntty
make build                  # build with version stamping (git commit + date)
make build-all              # also cross-compile for linux/amd64 + linux/arm64
```

## Usage

### Connect & Execute

```bash
# Connect to a remote host (deploys binary, starts daemon, forwards socket)
hauntty connect user@myserver

# Execute a command in the primary session
hauntty exec -s <sid> "apt update && apt upgrade -y"

# Auto-answer interactive prompts with "yes"
hauntty exec -s <sid> -y "rm -i *.log"

# Don't wait for completion (fire and forget)
hauntty exec -s <sid> -w=false "make build"

# Poll for completion later
hauntty poll -s <sid> --seq 3
```

### Parallel Sessions

```bash
# Spawn a new session (returns new SID)
hauntty spawn -s <primary-sid>
# HAUNTTY_SID=e1ec8da8

# Execute on the spawned session
hauntty exec -s <primary-sid> --target e1ec8da8 "cd /opt/frontend && npm run build"

# Execute on primary simultaneously — true parallel execution
hauntty exec -s <primary-sid> "cd /opt/backend && go build ."

# Each session has its own CWD, env, shell state, and log
hauntty exec -s <primary-sid> --target e1ec8da8 "pwd"   # /opt/frontend
hauntty exec -s <primary-sid> "pwd"                      # /opt/backend

# Kill spawned session (primary unaffected)
hauntty kill <primary-sid> --target e1ec8da8
```

### Read & Observe

```bash
# Read output from a specific command
hauntty read -s <sid> --seq 3 --stream stdout
hauntty read -s <sid> --seq 3 --stream stderr

# Read with offset and limit (pagination)
hauntty read -s <sid> --seq 3 --stream stdout --offset 100 --limit 50

# Peek at live PTY output (last N lines, ANSI-stripped)
hauntty peek -s <sid> -n 50

# Spectate session in real time (read-only)
hauntty attach <sid>
```

### Session Management

```bash
# List all active sessions (* = primary, [alive]/[dead] status)
hauntty list

# Kill a session
hauntty kill <sid>                         # primary — shuts down daemon
hauntty kill <primary> --target <spawned>  # spawned — daemon continues

# Remove stale sockets
hauntty clean

# Completely remove hauntty from a remote host
hauntty uninstall myserver

# Check version (includes git commit and build date)
hauntty version
```

### Corpus Log

```bash
# Dump entire corpus (pipe to jq for pretty-printing)
hauntty corpus
hauntty corpus | jq .

# Filter by time window
hauntty corpus --since 1h

# Filter by host or session
hauntty corpus --host myserver
hauntty corpus --sid 38b25f55

# Show only failed commands
hauntty corpus --failed

# Combine filters (failed commands on myserver in the last 24h)
hauntty corpus --host myserver --failed --since 24h
```

## Agent Integration

Add this block to your agent's context file (`CLAUDE.md`, `.cursorrules`, `GEMINI.md`, or equivalent). It teaches the agent to use hauntty and the metadata-first pattern in ~130 tokens:

```markdown
## hauntty (remote shell)
Use `hauntty` for all remote host operations. Never use raw `ssh host "cmd"`.
- `hauntty connect user@host` → returns SID and socket path
- `hauntty exec -s SID "cmd"` → returns metadata: seq, rc, stdout_lines, stderr_lines, cwd
- `hauntty read -s SID --seq N --stream stdout [--offset O --limit L]`
- `hauntty peek -s SID -n 20` → last N raw PTY lines

**Token discipline:** exec returns stdout_lines — if 0, don't read. Count before read (`wc -l`, `grep -c`). Filter at source (`| tail -N`, `| grep -v noise`). Never run broad queries without piping through tail/grep/wc first.
```

That's all the agent needs. The persistent session, structured metadata, and selective reading happen automatically — the block just teaches the agent to use the metadata instead of dumping blind.

## Requirements

- Go 1.22+ (build)
- SSH access to remote hosts (key-based auth recommended)
- Linux on the remote host (PTY + /proc filesystem required)
- `make` (optional, for version-stamped builds)

## Dependencies

- [`github.com/creack/pty`](https://github.com/creack/pty) — PTY allocation (pure Go, no CGO)
- [`github.com/spf13/cobra`](https://github.com/spf13/cobra) — CLI framework

## Documentation

- [Wire Protocol](docs/protocol.md) — message format, operations, process states
- [Architecture](docs/architecture.md) — data layout, component design, key decisions

## Security Notice

hauntty relies on SSH for authentication and transport encryption, but **does not implement its own authentication on the daemon socket**. Any process on the remote host that can access the Unix socket can execute commands as the daemon's user. This means an authenticated user on a shared host could escalate beyond their intended authorization perimeter through the socket.

Additional gaps in v0.1.0: command text is persisted as plaintext on disk (risk if commands contain secrets), and `--yes` mode confirms prompts without discrimination.

**Recommendation:** Use hauntty only in environments where you trust all users on the remote host — single-tenant servers, dedicated infrastructure, or hosts where SSH access already implies full trust. Do not deploy on shared multi-tenant hosts until socket authentication is implemented.

These are the top priorities for the next release. See [TODO.md](TODO.md) for the full roadmap.

## License

Apache License 2.0 — see [LICENSE](LICENSE)
