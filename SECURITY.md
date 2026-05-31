# Security Policy

## Supported versions

hauntty is pre-1.0. Security fixes land on the latest minor release.

| Version | Supported |
| ------- | --------- |
| 0.2.x   | ✅        |
| < 0.2   | ❌        |

## Threat model

hauntty runs a daemon on a remote host that owns persistent shell sessions, and
talks to it over a Unix socket forwarded through SSH. Its security posture today:

- **Authentication & transport** are delegated to SSH. hauntty does not open any
  network port; the socket is reached only through the SSH-forwarded channel.
- **The daemon socket is owner-only (`0600`)** and lives under `~/.hauntty/<sid>/`
  with a `/tmp/hauntty-<sid>.sock` symlink for convenience. There is **no
  application-level authentication handshake yet**: any process running as the
  daemon's own UID that can open the socket can execute commands through it.
- **Command files** (`cmd.N.pending`) are written `0600` and removed as soon as
  the shell wrapper consumes them. A command's captured `stdout`/`stderr`/`rc`
  persist under the session directory until the session is killed.

### What this means in practice

Treat the **UID the daemon runs as** as the trust boundary. hauntty is intended
for single-tenant servers, dedicated infrastructure, or hosts where SSH access
already implies full trust of that account.

**Do not** deploy hauntty where untrusted local users share the daemon's UID, or
rely on it as a privilege boundary, until socket authentication is implemented.

### Known gaps (tracked in [TODO.md](TODO.md))

- No socket authentication handshake (nonce/token). Planned.
- `--yes` confirms any detected prompt without discrimination — do not combine
  it with commands that could prompt for a destructive confirmation.
- Captured command output remains on disk for the session's lifetime.

## Reporting a vulnerability

Please report security issues **privately**, not via public issues:

- Preferred: open a [GitHub private security advisory](https://github.com/sch0tten/hauntty/security/advisories/new).
- Or email the maintainer: `schotten83@gmail.com`.

Include a description, affected version, and reproduction steps if possible.
You can expect an acknowledgment within a few days. Please allow time for a fix
before any public disclosure.
