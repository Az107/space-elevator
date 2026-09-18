# space-elevator

Podman-first deployment tool. Single binary that is both a CLI and a web
dashboard: deploy compose stacks, plain source repos, static sites, or
serverless-style functions from git or an uploaded archive, get Traefik
routing and a REST API.

- **Web/CLI/API roadmap**: see [PLAN.md](./PLAN.md) — v2 (account
  management, env/secrets, advanced builds, REST API with PATs) is done.
- **API docs**: [docs/api.md](./docs/api.md)
- **Writing functions**: [docs/functions.md](./docs/functions.md)

## Requirements

- Go 1.22+ (build only)
- Podman 4.x with the user API socket enabled
  (`systemctl --user enable --now podman.socket`)
- Traefik (rootful, with a file provider watching
  `~/Infra/traefik/rootful-dynamic` by default)

The podman socket is auto-detected at `/run/user/<uid>/podman/podman.sock`;
override with `PODMAN_SOCKET=...`.

## Build & install

```sh
make install           # builds dist/ and installs to ~/.local/bin
```

`~/.local/bin` is chosen over `/usr/bin` on purpose: the tool runs
rootless against your own podman socket, so a system-wide path needs root
without buying anything. If you prefer `/usr/bin`, run
`sudo install -m 0755 dist/space-elevator /usr/bin/` and adjust the unit
below.

## Running as a systemd user service

```sh
make install-service   # installs the unit + systemctl --user enable --now
```

The unit (`deploy/space-elevator.service`) runs
`~/.local/bin/space-elevator serve --addr 0.0.0.0:8080` as your user,
restarts on failure, and pulls in `podman.socket` so the dashboard comes
up with a working runtime. With user lingering enabled
(`loginctl enable-linger`) it starts at boot without a login session.

Everyday commands:

```sh
systemctl --user status space-elevator    # / start / stop / restart
journalctl --user -u space-elevator -f    # logs
```

To ship a new build: `make install-service` (or `make install` +
`systemctl --user restart space-elevator`).

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
cmd/space-elevator/   CLI entry point + commands
internal/deployer/    shared deploy pipeline (CLI, web, API)
internal/podman/      Docker-API compatible client for Podman
internal/composer/    custom Go compose runtime
internal/store/       SQLite (apps, env/secrets, users, API tokens)
internal/web/         dashboard + REST API (/api/v1)
deploy/               systemd user unit
```

## Quick start

```sh
./dist/space-elevator podman ps
./dist/space-elevator podman images
./dist/space-elevator podman info
```

Then open the dashboard (default self-route:
`https://elevator.albruiz.dev/`) and set the admin password on first
visit.
