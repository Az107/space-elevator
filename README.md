# space-elevator

Podman-first deployment tool. Single binary that serves as both a CLI and (eventually) a web dashboard.

## Status

**Phase 1**: Scaffolding + Podman client — ✅ in progress

See [PLAN.md](./PLAN.md) for the full roadmap.

## Quick start

```bash
make build
./dist/space-elevator podman ps
./dist/space-elevator podman images
./dist/space-elevator podman info
```

## Requirements

- Go 1.22+
- Podman 4.x with the API socket enabled

The tool auto-detects the user socket at `/run/user/<uid>/podman/podman.sock`. Override with `PODMAN_SOCKET=...` if needed.

## Layout

```
cmd/space-elevator/   CLI entry point + commands
internal/podman/      Docker-API compatible client for Podman
internal/config/      Paths + defaults
web/                  Embedded web UI (Phase 4)
templates/            App template YAML (Phase 5)
```