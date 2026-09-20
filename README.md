# space-elevator

Podman-first deployment tool. Single binary that is both a CLI and a web
dashboard: deploy compose stacks, plain source repos, static sites, or
serverless-style functions from git or an uploaded archive, get Traefik
routing and a REST API.

- **Web/CLI/API roadmap**: see [PLAN.md](./PLAN.md) — v2 (account
  management, env/secrets, advanced builds, REST API with PATs) is done.
- **Configuration reference**: [docs/configuration.md](./docs/configuration.md)
- **API docs**: [docs/api.md](./docs/api.md)
- **Writing functions**: [docs/functions.md](./docs/functions.md)

## Requirements

- Go 1.26+ (build only)
- Podman 4.x with the user API socket enabled
  (`systemctl --user enable --now podman.socket`)
- Traefik is **optional**: with it you get automatic HTTPS and `/app/<name>/`
  routes; without it apps are reachable on their published host ports.

The tool ships with neutral defaults — nothing points at a specific host. Run
`space-elevator setup` once to configure it.

## Build & install

```sh
go build -o dist/space-elevator ./cmd/space-elevator
install -m 0755 dist/space-elevator ~/.local/bin/space-elevator
```

`~/.local/bin` is chosen over `/usr/bin` on purpose: the tool runs rootless
against your own Podman socket, so a system-wide path needs root without buying
anything. (`make install` does the same on hosts that have `make`.)

## Quick start

```sh
space-elevator setup          # guided: exposure mode, config file, admin, service
space-elevator doctor         # verify Podman, config, Traefik, DNS, systemd
space-elevator serve          # or: space-elevator service install
```

`setup` detects Podman, asks whether you want Traefik + HTTPS, direct host
ports, or an existing proxy, writes a commented
`~/.config/space-elevator/config.yaml`, optionally creates the admin account,
and optionally installs the systemd user service. Everything is editable later
— see [docs/configuration.md](./docs/configuration.md).

## Running as a systemd user service

```sh
space-elevator service install --linger   # install + enable + start + boot
space-elevator service status
space-elevator service logs -f
space-elevator service restart
space-elevator service uninstall          # stops/disables/removes, keeps data
```

The unit runs `space-elevator serve --addr <bind_addr>` as your user and pulls
in `podman.socket`. With lingering enabled it starts at boot without a login
session. Environment overrides can live in `~/.config/space-elevator/env`,
which the unit loads automatically.

## Configuration

All settings come from (highest priority first) CLI flags, `SPACE_ELEVATOR_*`
environment variables, `~/.config/space-elevator/config.yaml`, then built-in
defaults.

```sh
space-elevator config init --from-legacy   # write a config, seeded from an existing setup
space-elevator config show                 # effective values + env overrides
space-elevator config validate
space-elevator config path
```

Full key-by-key reference, exposure modes, and a minimal Traefik config:
**[docs/configuration.md](./docs/configuration.md)**.

## Audit log

Security-relevant actions (logins and failures, account/password and API
token changes, app deploys/redeploys, removals, renames, env/secret and
domain edits) are recorded to an append-only `audit_events` table in the
state DB and emitted as one JSON line per event on stderr. Under the
systemd unit that means journald:

```sh
journalctl --user -u space-elevator | grep '"audit":true'
```

Events carry actor type/id/label (`user`, `token`, `cli`, `system`,
`anonymous`), action, target, outcome, client IP, and User-Agent. Client
IPs are taken from `CF-Connecting-IP` / `X-Forwarded-For` only when the
direct peer is a local/private proxy, so a direct client cannot spoof
them. Secret and password values are never written. Events older than 90
days are pruned by the hourly GC sweep.

## Commands

```sh
space-elevator setup                    # guided first-run configuration
space-elevator doctor                   # host diagnostics
space-elevator service <verb>           # systemd user unit management
space-elevator config <verb>            # inspect/initialize configuration

space-elevator apps deploy <git-url> --name demo   # compose stack
space-elevator apps deploy <git-url> --image node:20 \
  --build-cmd "npm ci && npm run build" --run-cmd "npm start" --port 3000
space-elevator apps deploy <repo> --kind function --language python \
  --runtime 3.12 --entrypoint handler.py:handler   # serverless function
space-elevator apps upload site.tar.gz              # quick deploy an archive
space-elevator apps upload fn.zip --kind function --language node --entrypoint index.js:handler
space-elevator apps list | logs | start | stop | restart | rename | redeploy | remove
space-elevator user reset-password                 # lockout recovery
space-elevator serve                               # web dashboard (the service runs this)
```

The dashboard's **New app** wizard covers all of this: pick a kind
(web / function / container) and a source (git / upload). The apps page
also keeps a one-drop quick-deploy zone for archives.

### Functions

Function apps get a generated Python or Node HTTP adapter that maps each
request to an API Gateway-style event and calls your handler
(`handler.py:handler`, `index.js:handler`). Dependencies in
`requirements.txt` / `package.json` are installed at build time. The
adapter exposes `/__se/health` and containers carry a
`space-elevator.kind=function` label; scale-to-zero is planned and the
storage/labels are already in place, but functions run always-on today.

See **[docs/functions.md](./docs/functions.md)** for handler examples, the
event/response contract, dependencies, and how to test an adapter locally.

## Layout

```
cmd/space-elevator/   CLI entry point + commands (setup, doctor, service, config)
internal/deployer/    shared deploy pipeline (CLI, web, API)
internal/podman/      Docker-API compatible client for Podman
internal/composer/    custom Go compose runtime
internal/store/       SQLite (apps, env/secrets, users, API tokens)
internal/web/         dashboard + REST API (/api/v1)
internal/service/     systemd user unit rendering/management
internal/config/      layered config (flags > env > file > defaults)
deploy/               reference systemd user unit
```
