# AGENTS.md — start here

Entry point for any new session working on **space-elevator** (often
abbreviated **SE** / **SP**). Read this before touching code.

## What this project is

A self-hosted, Podman-first PaaS. A single Go binary is simultaneously:

- a **CLI** (`space-elevator …`),
- a **web dashboard** (HTMX pages), and
- a **REST API** (`/api/v1`, Personal Access Tokens).

It deploys **compose stacks**, plain **source repos**, **static sites**,
and serverless-style **functions** from git or an uploaded archive, and
writes **Traefik** routing + Let's Encrypt config for each app. State
lives in SQLite; runtime state lives in Podman, addressable by the
`space-elevator.*` container labels.

## Entry points (read these first)

| Concern | File |
|---|---|
| CLI root, command registration | `cmd/space-elevator/main.go` |
| Service command (what systemd runs) | `cmd/space-elevator/commands/serve.go` |
| All HTTP routes (dashboard + `/api/v1`) | `Server.Routes()` in `internal/web/server.go` |
| Shared deploy pipeline (CLI/web/API) | `internal/deployer/deployer.go` |
| Custom compose runtime | `internal/composer/` |
| SQLite store + migrations | `internal/store/`, `internal/store/migrations/` |
| Builders (git/archive/static/function) | `internal/builder/` |
| Paths, env, defaults | `internal/config/config.go` |
| Route reconciliation on startup | `internal/web/reconcile.go` |
| Roadmap / API / functions docs | `PLAN.md`, `docs/api.md`, `docs/functions.md` |

## Runtime facts

- **Dashboard**: `https://elevator.albruiz.dev/`
- **Apps** are exposed under `https://elevator.albruiz.dev/app/<app>/`
  (Traefik `Host` + `PathPrefix`, prefix stripped). The app name in the
  URL can differ from the display name after a rename.
- **State DB**: `~/.local/state/space-elevator/space-elevator.db`
- **Config file**: `~/.config/space-elevator/config.yaml` (override with
  `SPACE_ELEVATOR_CONFIG` or `--config`). Precedence is
  flags > `SPACE_ELEVATOR_*` env > file > neutral defaults. Reference:
  `docs/configuration.md`. **Defaults are host-agnostic** (no public host,
  no Traefik); this host preserves its old values via
  `space-elevator config init --from-legacy`.
- **Apps root**: `~/apps` (drops + checkouts)
- **Traefik dynamic dir**: `~/Infra/traefik/rootful-dynamic/*.yml`
- **Podman socket**: auto-detected; override with `PODMAN_SOCKET=…`
- **Service**: `space-elevator.service` (systemd user unit),
  `ExecStart=%h/.local/bin/space-elevator serve --addr 0.0.0.0:8080`
- `serve` also writes the dashboard's own Traefik file, kicks off
  background route reconciliation, and runs hourly GC + audit pruning.

## Build / test / operate

**Toolchain**: Go 1.26 (`go.mod` says `go 1.26.0`; the README's
"Go 1.22+" is stale). No CI and no `make lint` target exists despite
`lint` being listed in `.PHONY` — use `go vet ./...`.

**`make` is not installed on this host.** Do not depend on it; run the
underlying `go`/`install`/`systemctl` commands directly. The `Makefile`
targets exist for hosts that have `make`, but they are only wrappers.

```sh
# Build -> dist/space-elevator
go build -o dist/space-elevator ./cmd/space-elevator

# Unit tests (fast, hermetic — no Podman/network needed)
go test ./...
go test ./internal/web/ -run TestWizard   # single package / test
go vet ./...

# Install the fresh binary to ~/.local/bin
install -m 0755 dist/space-elevator ~/.local/bin/space-elevator

# Ship a new build and load it (build + install + restart)
go build -o dist/space-elevator ./cmd/space-elevator \
  && install -m 0755 dist/space-elevator ~/.local/bin/space-elevator \
  && systemctl --user restart space-elevator

systemctl --user status space-elevator
journalctl --user -u space-elevator -f
journalctl --user -u space-elevator | grep '"audit":true'
```

`make install` = build + `install -m 0755 …`; `make install-service` =
that plus copying `deploy/space-elevator.service` to
`~/.config/systemd/user/`, `systemctl --user daemon-reload`, and
`systemctl --user enable --now space-elevator`. If you do install `make`
(`sudo apt install make`), the targets still work unchanged.

## Consumer setup commands

New operator-facing commands (also usable to diagnose this host):

```sh
space-elevator setup            # huh TUI wizard; --yes + flags for non-TTY/CI
space-elevator doctor           # Podman/config/Traefik/DB/DNS/systemd checks
space-elevator service <verb>   # install|uninstall|start|stop|restart|status|logs
space-elevator config <verb>    # path|show|init [--from-legacy]|validate
```

The config file is layered (flags > env > file > defaults) and generated
with inline docs. The systemd unit is rendered from `internal/service`
(embedded template) using the resolved `bind_addr`; `service install`
writes it, `daemon-reload`s and optionally enables lingering.

**Upgrading a pre-config-file host** (this host): neutral defaults mean the
old hard-coded `elevator.albruiz.dev`/Traefik values must be captured
before a restart, or routes stop being written:

```sh
space-elevator config init --from-legacy   # recovers host/resolver/gateway
space-elevator service restart
space-elevator doctor
```

## Embedded assets (rebuild required)

HTML, CSS, JS, SQL migrations, and function templates are compiled into
the binary with `go:embed` (`internal/web/embed.go`,
`internal/store/store.go`, `internal/builder/function.go`). Editing
`internal/web/pages/*`, `internal/web/static/*`,
`internal/store/migrations/*.sql`, or
`internal/builder/function_templates/*` has **zero effect until you
rebuild and reinstall**. There is no hot reload and no dev server;
`serve` runs the compiled binary. Migrations are applied from the
embedded FS at startup, so a new migration only takes effect after a
rebuild + service restart.

## MANDATORY verification after every change

**Unit tests are not enough.** `go test ./...` passes even when the live
deploy path is broken, because the deploy pipeline needs a running
Podman socket, generated Traefik config, and reachable containers. A
recent "kinds/wizard" refactor broke real app deployment while the whole
suite stayed green — apps became **unavailable from the outside**. Treat
that as the default failure mode and prove otherwise every time.

Do not declare a change done after `go test`. Run this end-to-end check:

1. **Sanity**: `go test ./...` and `go vet ./...` (or the lint used in
   the repo). Fix failures first.
2. **Ask for a deploy (build + restart the service).** Do not silently
   restart production yourself. Tell the user, e.g.:
   > "Please deploy: `go build -o dist/space-elevator ./cmd/space-elevator && install -m 0755 dist/space-elevator ~/.local/bin/space-elevator && systemctl --user restart space-elevator`"
   Wait for confirmation, then verify:
   `systemctl --user is-active space-elevator` and
   `curl -sS -o /dev/null -w '%{http_code}\n' https://elevator.albruiz.dev/`
   (expect `303` to `/login` or `200`).
3. **Create a NEW app** in space-elevator (not just a redeploy of an old
   one, which can mask a broken create path). Any of:
   ```sh
   ./dist/space-elevator apps deploy <small-public-repo> --name se-e2e-<rand>

   # or an archive:
   ./dist/space-elevator apps upload /tmp/site.tar.gz --name se-e2e-<rand>
   ```
   Then poll `./dist/space-elevator apps list` until it shows `running`.
4. **Check it works from the OUTSIDE** — over the public URL, through
   Traefik, not just against the container:
   ```sh
   curl -sS -o /dev/null -w '%{http_code}\n' \
     https://elevator.albruiz.dev/app/se-e2e-<rand>/
   curl -sS https://elevator.albruiz.dev/app/se-e2e-<rand>/ | head
   ```
   Expect `200` and real app content. A `404`/`502`/`503` means the
   route or container is broken even though the deploy "succeeded".
5. **Clean up** the test app:
   `./dist/space-elevator apps remove se-e2e-<rand>`
6. **Report** the exact commands run and the observed status codes/output.

If steps 2–4 can't be run (no host access, etc.), say so explicitly and
mark the change **unverified** — never imply it was tested.

## Conventions

- Match the surrounding Go style; keep the `cli/web/api` paths sharing
  the same `internal/deployer` pipeline rather than duplicating logic.
- Add/adjust unit tests next to the code you change (`*_test.go`).
- New schema changes go in `internal/store/migrations/` as a new
  numbered file; never edit an applied migration.
- Never commit unless the user asks. Never log secrets/tokens/passwords;
  the audit log deliberately stores only secret *keys*.
- When in doubt, read `PLAN.md` for the current roadmap/status before
  proposing a design.
