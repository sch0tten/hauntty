# Contributing to hauntty

Thanks for your interest in hauntty. This is a small, focused tool; contributions
that keep it small and focused are the most welcome.

## Development

hauntty is pure Go with two dependencies (`creack/pty`, `spf13/cobra`) and no CGO.

```bash
make build          # version-stamped local build
make build-all      # also cross-compile linux/amd64 + linux/arm64
go vet ./...
go test -race ./...  # the daemon tests drive real bash on a PTY (Linux only)
```

The daemon and its tests require **Linux** (PTY + `/proc`). The CLI builds and
runs anywhere Go does, but `connect`/`daemon` target Linux remote hosts.

## How it works (orientation)

- `protocol/` — line-delimited JSON wire types and the connection-safe codec.
- `daemon/procmon.go` — `/proc` reader. Command state is derived from the
  **terminal foreground process group** (`tpgid`), not "all bash children".
  Read the comments here before changing classification.
- `daemon/session.go` — PTY + bash, the exec wrapper (captures rc/cwd, publishes
  a readiness nonce), liveness, and self-healing `Restart`.
- `daemon/controller.go` — command monitoring (fast rc-poll → slow `/proc`),
  the hard deadline, prompt detection, and background-PID surfacing.
- `daemon/daemon.go` — socket listener, request routing, `wait`.
- `cmd/` — the cobra CLI.

See [docs/architecture.md](docs/architecture.md) and
[docs/protocol.md](docs/protocol.md) for the full picture.

## Pull requests

- Keep the working tree race-clean: `go test -race ./...` and `go vet ./...`
  must pass. CI runs both.
- Add a test for behavior changes. The daemon tests show how to drive a real
  session (`newTestController`, `runCmd`).
- Use [Conventional Commits](https://www.conventionalcommits.org/) for messages
  (`feat:`, `fix:`, `docs:`, `test:`, `ci:`, `chore:`).
- Update [CHANGELOG.md](CHANGELOG.md) under `## [Unreleased]`.
- For anything touching the socket, command files, or `--yes`, note the security
  impact (see [SECURITY.md](SECURITY.md)).

## Reporting bugs

Open an issue with the hauntty version (`hauntty version`), the remote OS, and a
reproduction. For security issues, follow [SECURITY.md](SECURITY.md) instead.
