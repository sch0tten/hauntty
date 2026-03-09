# Architecture

## Data Layout

```
~/.hauntty/
├── hauntty.log                # daemon log (all sessions)
├── corpus.jsonl               # centralized command log (all sessions, JSONL)
├── <primary-sid>/
│   ├── hauntty.sock           # unix socket (daemon listener)
│   ├── session.log            # YAML command history
│   ├── hauntty.pid            # bash PID
│   ├── .hauntty_wrapper.sh    # injected shell function
│   └── cmd.1/
│       ├── cmdline            # raw command text
│       ├── stdout             # captured stdout
│       ├── stderr             # captured stderr
│       └── rc                 # exit code (file existence = done)
├── <spawned-sid>/
│   ├── session.log            # independent log
│   ├── hauntty.pid
│   ├── .hauntty_wrapper.sh
│   └── cmd.1/ ...
```

## How It Works

1. **`hauntty connect user@host`** — detects remote architecture, SCPs the binary, starts `hauntty daemon` via nohup, creates an SSH tunnel forwarding the unix socket. Compares version strings to decide whether to redeploy.

2. **`hauntty daemon`** — allocates a PTY with a bash shell, injects the `__hauntty_exec` wrapper function that captures stdout/stderr using file descriptor redirection (not subshells — `cd`, `export`, and shell state persist). Listens on a unix socket for commands. Manages up to 16 parallel sessions.

3. **`hauntty exec`** — sends the command over the socket with optional session targeting. The Controller spawns a monitor goroutine that samples `/proc` every second — tracking process state, CPU usage, and I/O. Sends live status updates for long-running commands, detects interactive prompts via kernel wait channels, and reports completion with structured metadata.

4. **`hauntty spawn`** — creates a new session within the same daemon. The new session gets its own PTY, bash process, working directory, and command log. All sessions share the same unix socket — the `sid` field in requests routes to the correct session.

5. **`hauntty read`** — reads stored output files with optional offset and limit. The agent can read just the first 10 lines of a 10,000-line output, or skip to line 500.

6. **`hauntty uninstall host`** — stops all daemons, removes the binary, session data, and sockets.

## Key Design Decisions

- **FD redirection, not subshells** — the `__hauntty_exec` wrapper uses `exec 3>&1 4>&2` to save file descriptors, then redirects stdout/stderr to files. After the command completes, it restores the original fds. This preserves shell state (`cd`, `export`, functions) across commands.

- **Deterministic /proc monitoring** — no regex parsing of terminal output. The daemon reads `/proc/<pid>/stat` (process state byte), `/proc/<pid>/wchan` (kernel wait channel), and `/proc/<pid>/io` (I/O counters). This gives reliable process classification without heuristics.

- **Pipeline debouncing** — between pipeline stages (`cmd1 | cmd2`), there's a brief moment where bash has no children and is on `n_tty_read`. The monitor requires 2 consecutive ticks in this state before reporting a prompt, avoiding false positives.

- **Parallel via session isolation** — instead of multiplexing commands within one shell (which would break state), parallelism is achieved by spawning separate sessions. Each has its own PTY and bash process. Commands serialize within a session; sessions run in parallel.
