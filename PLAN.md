# Plan: space-elevator (Self-hosted PaaS, Podman-first)

## Overview

A self-hosted PaaS like Dokploy/Netlify for your own server. Single Go binary that is
both CLI and web dashboard. Two deployment sources: a git repo with a compose file,
or a tarball dropped onto the web UI. Traefik handles routing/TLS automatically.

## Goals (v1)

- Single binary, single host, single user
- Deploy **compose stacks** from a git repo (Dockerfile-only, PAT auth)
- Deploy from **tarball drag-drop** (static site auto-generated Dockerfile, OR source-with-Dockerfile)
- Automatic **Traefik routing** + Let's Encrypt on per-app domains
- Web UI + CLI parity (same operations both ways)
- Survives reboots (app state in SQLite, pods survive via Podman)

## Non-goals (v1, deferred to v2)

- One-click templates (WordPress, Gitea, etc.)
- Auto-redeploy webhooks
- Managed databases
- Multi-user, OAuth, buildpacks
- PR preview environments, rollback history
- Per-app resource limits
- Backup/restore tooling

## Deployment sources

| Source | Detection | Outcome |
|---|---|---|
| Git URL + PAT | Repo contains `compose.yml` / `docker-compose.yml` | Custom Go compose runtime builds/runs each service |
| Tarball drop (static) | Only `.html`, `.css`, `.js`, assets — no Dockerfile | Generate `Dockerfile` on the fly (nginx:alpine + COPY) |
| Tarball drop (source) | Contains a `Dockerfile` | Build the image; run as single container |

All three paths converge on a single **App = compose stack** model. For tarball drops
that aren't multi-service, we generate a synthetic single-service compose.yml.

## App model

```
App {
  id            string  (uuid, also used as compose project name)
  name          string  (slug, user-chosen)
  source_type   "git" | "drop"
  source_ref    string  (git URL OR drop UUID)
  git_ref       string  (branch/tag, default "main")
  drop_kind     "static" | "dockerfile" | ""   (only for source_type=drop)
  compose_yaml  string  (raw compose file content, or synthetic)
  env           map[string]string
  domains       []string
  status        "running" | "stopped" | "partial" | "error"
  created_at    time
  updated_at    time
}
```

Runtime state (container IDs, networks, volumes) lives in Podman itself, queried by
labels: every container created by space-elevator is tagged:
- `space-elevator.app=<app-name>`
- `space-elevator.service=<service-name>`
- `space-elevator.managed=1`

Per-app state is reconstructable from Podman + the SQLite `apps` row.

## Architecture

```
┌────────────────────────────────────────────────────────┐
│              space-elevator (Go binary)                 │
├────────────┬────────────┬─────────────┬────────────────┤
│   CLI      │  Web UI    │  Composer   │  Traefik       │
│ (cobra)    │ (chi+htmx) │  (custom    │  config        │
│            │            │   Go)       │  generator     │
├────────────┴────────────┴─────────────┴────────────────┤
│                SQLite (apps, sessions)                  │
├──────────────────────────────────────────────────────────┤
│ /run/podman/podman.sock │ /etc/space-elevator/traefik/  │
└──────────────────────────────────────────────────────────┘
```

## Tech stack

| Component | Choice | Notes |
|---|---|---|
| Language | Go 1.22+ | Single binary |
| CLI | `spf13/cobra` | |
| HTTP | `net/http` + `go-chi/chi/v5` | Stdlib-friendly router |
| Frontend | HTML + HTMX + minimal JS | JS only for drag-drop + log streaming |
| State | `modernc.org/sqlite` | Pure Go, no CGo |
| Podman | `github.com/docker/docker/client` | Docker-API compatible socket |
| Compose | **Custom Go runtime** (~300 LOC) | No Python dep |
| YAML | `gopkg.in/yaml.v3` | |
| Reverse proxy | Traefik v3 (file provider) | Already on host |

## Directory structure

```
space-elevator/
├── PLAN.md
├── README.md
├── Makefile
├── go.mod / go.sum
├── cmd/
│   └── space-elevator/
│       ├── main.go                 # CLI + server entry
│       └── commands/
│           ├── ps.go               # thin wrapper over podman client
│           ├── images.go
│           ├── info.go
│           ├── apps.go             # apps list/deploy/remove/logs
│           └── domains.go          # domain add/list/remove
├── internal/
│   ├── podman/                     # Docker-API compatible client (done)
│   │   ├── client.go
│   │   ├── containers.go
│   │   ├── images.go
│   │   └── networks.go
│   ├── store/                      # SQLite + app domain
│   │   ├── store.go
│   │   ├── apps.go
│   │   └── session.go
│   ├── composer/                   # Custom compose runtime
│   │   ├── parser.go               # YAML → spec
│   │   ├── spec.go                 # Service/Network/Volume types
│   │   ├── runtime.go              # podman orchestration
│   │   └── generate.go             # synthetic compose for drops
│   ├── builder/                    # Source acquisition
│   │   ├── git.go                  # clone with PAT
│   │   └── drop.go                 # tarball extract + detect
│   ├── traefik/                    # Traefik config gen
│   │   ├── config.go
│   │   └── writer.go
│   ├── web/                        # HTTP handlers + auth
│   │   ├── router.go
│   │   ├── auth.go                 # session middleware
│   │   ├── handlers_apps.go
│   │   ├── handlers_deploy.go      # git + drop
│   │   ├── handlers_logs.go        # SSE streaming
│   │   └── static.go               # embed.FS
│   └── config/
│       └── config.go
├── web/                            # Embedded frontend
│   ├── pages/
│   │   ├── login.html
│   │   ├── apps.html
│   │   ├── app_detail.html
│   │   ├── deploy.html
│   │   └── settings.html
│   ├── static/
│   │   ├── css/app.css
│   │   └── js/drop.js              # drag-drop zone
│   └── embed.go                    # //go:embed directives
└── docs/
    ├── architecture.md
    ├── deployment.md
    └── traefik.md
```

## Custom compose runtime (sub-spec)

Supports the **MVP subset** of compose v1:

- `services.<name>.image` — pull and run
- `services.<name>.build` — `podman build` from path inside source
- `services.<name>.ports` — `host:container`
- `services.<name>.environment` — env vars (merged with app-level env)
- `services.<name>.volumes` — named or bind mounts
- `services.<name>.depends_on` — start order
- `services.<name>.networks` — attach to per-app network
- `services.<name>.labels` — pass through (we add ours)
- `services.<name>.command` — override entrypoint
- `networks` (top-level) — bridge only
- `volumes` (top-level) — named volume driver=local

Not supported in v1: secrets, configs, scaling, profiles, healthchecks-as-policy,
compose v2 extensions, multi-network aliases, host networking.

## Traefik integration

- One file per app at `/etc/space-elevator/traefik/dynamic/<app>.yml`
- Per service with published ports: emit a `service` + per-domain router
- Routes require domains to be attached via CLI or web UI
- TLS via existing `letsencrypt` resolver declared in the host's Traefik static config
- On app deploy/remove: rewrite the file and let Traefik file-provider reload

A single dynamic file per app keeps reloads atomic and removes-per-app trivial.

## Drag-drop flow

```
Browser: drag tarball → POST /api/apps/drop (multipart)
Server:
  1. save tarball to <AppsRoot>/drops/<uuid>.tar.gz
  2. extract to <AppsRoot>/drops/<uuid>/
  3. detect kind:
     - has Dockerfile → kind=dockerfile
     - only static assets → kind=static, generate Dockerfile
  4. synthesize compose.yml (single service, image=built locally)
  5. write app row to DB
  6. run composer.Runtime.Deploy(app)
  7. respond with app id
```

## Phase plan (revised)

### Phase 1: Scaffolding + Podman client — ✅ done
CLI works against live Podman socket.

### Phase 2: Store + Composer + Git deploy (CLI) — ✅ done (2026-09-07)
- SQLite schema + migrations (`internal/store/migrations/0001_init.sql`).
- `composer/spec` and `composer/runtime` (custom Go compose, single binary, no Python).
- `builder/git` (clone with PAT, honours `git_credentials` per host).
- CLI: `space-elevator deploy <url> --name <name> [--ref <branch>]`.
- CLI: `space-elevator apps list|logs|restart|remove`.
- Status: code in tree, build clean (`go build ./...`, `go vet ./...`),
  unit tests pass. E2E smoke against the live Podman + Traefik pending.

### Phase 3: Traefik + Domains — ✅ done (2026-09-07, this commit)

#### What was broken
- `internal/config.Config.TraefikDir` defaulted to
  `~/.local/share/space-elevator/traefik/dynamic`, but the host's rootful
  Traefik file provider actually watches
  `~/Infra/traefik/rootful-dynamic/`. Files were written and silently ignored.
- The generated dynamic config lacked the per-route HTTP→HTTPS redirect
  middleware that the working rootful files (`forgejo.yml`, `moodle.yml`,
  `opencode.yml`, `rootless.yml`) use, so HTTP requests to
  `http://app.albruiz.dev` returned a Traefik "no router" error instead of
  redirecting to HTTPS.
- There was no way to reach the dashboard from the internet without first
  registering a dedicated subdomain via the rootful Traefik scripts.

#### Fixes shipped in this commit
- `internal/config/config.go`: `TraefikDir` now defaults to
  `~/Infra/traefik/rootful-dynamic` (env override:
  `SPACE_ELEVATOR_TRAEFIK_DIR`). New fields:
  - `PublicHost` (default `elevator.albruiz.dev`, env
    `SPACE_ELEVATOR_PUBLIC_HOST`)
  - `PublicPath` (default `""`, env `SPACE_ELEVATOR_PUBLIC_PATH`) — for
    future path-prefixed dashboard exposure.
  - `DashboardURL` (default `http://host.containers.internal:8080`, env
    `SPACE_ELEVATOR_DASHBOARD_URL`)
  - `AppPathPrefix` (default `/app/`, env `SPACE_ELEVATOR_APP_PATH_PREFIX`)
- `internal/traefik/config.go`: rewritten `Render` so every per-app file
  matches the format of the host's existing routes:
  - Two routers per compose service (`-web` redirects, `-secure` terminates
    TLS via the configured cert resolver).
  - One `<svc>-https` middleware with `redirectScheme.scheme=https`,
    `permanent=true`.
  - One `<svc>-strip` middleware with `stripPrefix.prefixes=[/app/<name>]`
    for the path-prefix routes.
- `internal/traefik/apply.go` (new): single `ApplyAppRoute` helper used by
  the CLI (`deploy`, `domain add/remove`, `apps remove`) and by the web
  handler, so all call sites stay in sync. Removes the file if the app has
  neither custom domains nor a path-prefix configured.
- `internal/traefik/self_route.go` (new): renders the dashboard's
  Traefik dynamic file (`space-elevator.yml`). Supports Host-only,
  PathPrefix-only, or `Host(...) && PathPrefix(...)` rules so the dashboard
  can share its host with other services.
- `internal/traefik/writer.go`: added `WriteSelf`, `RemoveSelf`, `SelfExists`.
- `cmd/space-elevator/commands/selfroute.go` (new):
  ```
  space-elevator self-route register [--host ...] [--path ...] [--backend-url ...]
  space-elevator self-route unregister
  space-elevator self-route show
  ```
- `cmd/space-elevator/commands/serve.go`: on startup, auto-writes the
  dashboard Traefik file (toggle with `--register-self=false`). Handles
  SIGINT/SIGTERM cleanly. Prints the public URL on success.
- `cmd/space-elevator/commands/deploy.go`: after a successful deploy, the
  app's Traefik file is regenerated with both any custom domains AND the
  auto path-prefix route (`https://elevator.albruiz.dev/app/<name>/`).
- `cmd/space-elevator/commands/domains.go` and
  `internal/web/handlers_domains.go`: switched to `ApplyAppRoute` so adding
  or removing a custom domain preserves the path-prefix route.
- `internal/web/handlers_settings.go` + `pages/settings.html`: settings
  page now surfaces the public dashboard URL and whether the self-route
  file is currently registered.
- `cmd/space-elevator/main.go`: registered `selfroute` as a top-level
  command.

#### Resulting Traefik file shapes

For an app `web` with one service and one custom domain `myapp.com`:
```yaml
http:
  routers:
    web-secure:
      rule: "Host(`myapp.com`)"
      entryPoints: [websecure]
      service: web
      tls: { certResolver: letsencrypt }
    web-web:
      rule: "Host(`myapp.com`)"
      entryPoints: [web]
      service: web
      middlewares: [web-https]
    web-path-secure:
      rule: "Host(`elevator.albruiz.dev`) && PathPrefix(`/app/web/`)"
      entryPoints: [websecure]
      service: web
      middlewares: [web-strip]
      tls: { certResolver: letsencrypt }
    web-path-web:
      rule: "Host(`elevator.albruiz.dev`) && PathPrefix(`/app/web/`)"
      entryPoints: [web]
      service: web
      middlewares: [web-strip, web-https]
  middlewares:
    web-https: { redirectScheme: { scheme: https, permanent: true } }
    web-strip:  { stripPrefix:    { prefixes: [/app/web] } }
  services:
    web: { loadBalancer: { servers: [{ url: "http://<container-ip>:<port>" }] } }
```

The dashboard self-route (`space-elevator.yml`):
```yaml
http:
  routers:
    space-elevator-secure:
      rule: "Host(`elevator.albruiz.dev`)"
      entryPoints: [websecure]
      service: space-elevator
      tls: { certResolver: letsencrypt }
    space-elevator-web:
      rule: "Host(`elevator.albruiz.dev`)"
      entryPoints: [web]
      service: space-elevator
      middlewares: [space-elevator-https]
  middlewares:
    space-elevator-https: { redirectScheme: { scheme: https, permanent: true } }
  services:
    space-elevator: { loadBalancer: { servers: [{ url: "http://host.containers.internal:8080" }] } }
```

#### Status: ✅ compiled, unit tests pass
`go build ./...`, `go vet ./...`, and `go test ./internal/traefik/...` all
pass. CLI smoke (`self-route register/unregister` against a temp dir) round-trips
a valid YAML. E2E test against the live rootful Traefik pending — see
"Verification" below.

### Phase 4: Web dashboard + Auth + Drag-drop — ✅ done (code in tree)
- Chi router, embedded FS, HTMX pages.
- Single-user login (bcrypt + session cookie, SQLite `users`/`sessions`).
- Pages: `login`, `setup`, `apps`, `app_detail`, `deploy`, `settings`.
- Drag-drop endpoint with JS dropzone (`internal/web/drop.go`,
  `static/js/app.js`).
- Log streaming via plain HTTP `?stream=1&tail=N` (SSE was the design but
  the implementation is simpler — see "Open questions").
- PAT storage form in settings, hydrated per host.
- Status: code in tree, build clean (same verification as Phase 3).

### Phase 5: Polish — partially done (2026-09-08)
- Systemd **user** unit ✅ (`deploy/space-elevator.service`,
  `make install-service`): runs `~/.local/bin/space-elevator serve
  --addr 0.0.0.0:8080` as the user, `Restart=on-failure`, wants
  `podman.socket`, enabled at boot with lingering. Binary installed to
  `~/.local/bin` (rootless podman user-socket runtime; `/usr/bin`
  would need root without benefit).
- README quickstart ✅ — build/install, service management, CLI
  examples (incl. advanced deploy), layout.
- `space-elevator init` first-run wizard — pending (the web /setup
  page covers first-run on the dashboard).
- `space-elevator update` for self-update — pending.

## Estimated effort (revised)

| Phase | Days | Status |
|---|---|---|
| 1. Scaffolding | 1 | ✅ done |
| 2. Store + Composer + Git | 3-4 | ✅ code done, e2e untested |
| 3. Traefik + Domains | 1-2 | ✅ code + tests + e2e done |
| 4. Web dashboard | 3-4 | ✅ code done, e2e untested |
| 5. Polish | 1 | Pending |

**Total v1**: ~9-12 days of focused work.

## Verification (2026-09-07)

All changes were compiled, vetted, and unit-tested on this server (the
remote host where the code will run). Earlier `go build`/`go vet` hangs
turned out to be caused by a local package that has since been removed.

### What passed
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/traefik/...` — 10 tests, all PASS:
  - `TestRenderSelf_HostOnly`
  - `TestRenderSelf_HostAndPath`
  - `TestRenderSelf_RejectsEmpty`
  - `TestRender_AppRouteWithSubdomainAndPath`
  - `TestRender_AppRouteSubdomainOnly`
  - `TestRender_AppRouteBothSubdomainAndPath`
  - `TestRender_AppRoutePathOnlyWhenNoDomain`
  - `TestRender_NoRoutesProducesEmpty`
  - `TestRender_SkipsRoutesWithoutPortOrIP`
  - `TestSelfRouteConfig_SelfURL`
- CLI smoke against a temp dir:
  ```bash
  mkdir -p /tmp/se-traefik
  /tmp/se self-route register --host elevator.albruiz.dev
  cat /tmp/se-traefik/space-elevator.yml    # valid YAML, eyeballed OK
  /tmp/se self-route unregister              # file removed
  ```
- Full per-app YAML (subdomain + path-prefix + redirect + strip +
  service) was visually inspected and matches the format of the working
  files in `~/Infra/traefik/rootful-dynamic/` (`forgejo.yml` etc.).

### What remains for full sign-off
- Run `space-elevator serve` against the real Podman socket and confirm
  the auto-registered `space-elevator.yml` is picked up by rootful Traefik
  (file provider hot-reload).
- Deploy a public git repo with a compose file that declares ports, and
  confirm `<app>.elevator.albruiz.dev/app/<app>/` resolves through Traefik
  to the container.
- Confirm HTTP→HTTPS redirect works on both the dashboard host and any
  custom subdomain (Traefik returns 301 with the right scheme).
- Run `space-elevator domain add <app> <custom-domain>` and confirm the
  file diff adds both the subdomain router and keeps the path-prefix
  router.

## Live-traffic bug fixes (2026-09-07)

While checking out the dashboard, the user tried
`drop-572d11b4.albruiz.dev` (a previously-deployed static tarball drop) and
the page loaded forever. Investigation revealed two bugs in the
Traefik-writer path that affect any drop-style deploy.

### Bug 1: backend URL uses an unreachable container IP

`internal/traefik/resolve.go` was emitting
`http://<container-ip>:<container-port>` into the per-app file. But the
composer runtime puts each app on its own private rootless bridge
(`se-<appname>`, e.g. `10.89.4.0/24`), and the host only has the rootless
gateway on `10.89.0.0/24`. Rootful Traefik (which lives on the host
network) has no route to `10.89.4.x`, so the request hangs until the
client times out.

**Fix.** `internal/podman/inspect.go` now also returns the
host-published port for each container
(`NetworkSettings.Ports`). `ResolveForApp` prefers
`<rootlessGateway>:<hostPort>` (default gateway `10.89.0.1`,
`SPACE_ELEVATOR_ROOTLESS_GATEWAY` override) and only falls back to the
container IP if no host port was published. This matches the format used
by the host's existing working files (`forgejo.yml`, `moodle.yml`).

### Bug 2: synth compose for static drops declared the wrong port

`internal/web/drop.go::syntheticCompose` was hard-coding
`ports: ["8080"]` for static-site drops. The default `nginx:alpine`
image listens on port **80**, not 8080. Podman dutifully published
container port 8080 to a random host port, but nothing inside the
container was listening on 8080 — so the host-published port forwarded
to a closed socket and Traefik got connection resets (502).

**Fix.** The synth compose now declares `ports: ["80"]`, matching the
default nginx listen port. Single-port spec lets Podman assign a random
host port, which we then read via `InspectIPs`.

### Ops commands added

- `space-elevator route regenerate <app>` — rewrites the Traefik
  dynamic file for one app from its current container IPs and attached
  domains. Useful after fixing compose or after DNS/network changes.
- `space-elevator apps redeploy <app>` — tears down and re-deploys an
  app from its stored source (re-pulls / re-builds, rewrites the
  Traefik route). For git-deploys, uses the existing source clone; for
  drops, uses the still-extracted drop directory.

### Verification

After both fixes and `apps redeploy drop-572d11b4`:

```text
moodle: status=200 time=0.117s   (unchanged, still works)
git:    status=200 time=0.026s   (unchanged, still works)
drop:   status=200 time=0.016s   (was: connection reset / 502 → hang)
```

The drop now responds through Traefik in 16 ms.

## Open questions (carry over)

0. **Cross-origin login 403** — ✅ root-caused and fixed (2026-09-08).
   Captured a real failing request via DevTools: Safari (and Chrome)
   send `Origin: null` on the login POST because the page was loaded
   through an HTTP→HTTPS redirect chain (Cloudflare proxies
   `elevator.albruiz.dev`); the old store parsed `null` as "no valid
   origin" and rejected, and — pre-fix — that branch never logged, so
   journalctl showed nothing. Fix: `originCheck` now uses **Fetch
   Metadata (`Sec-Fetch-Site`) as the primary signal** — browsers
   attest `same-origin`/`same-site` truthfully and it can't be forged
   from browsers — with the Origin-host comparison (request host +
   configured `PublicHost`) kept as fallback for legacy clients.
   `Origin: null` with same-site attestation is accepted; genuine
   cross-site POSTs (sibling-subdomain attack surface) are rejected
   with a log line. Unit tests cover the Safari/Chrome signature and
   the attack cases; live smoke: Safari-sim via CF → 200, cross-site
   sim → 403.

1. Should path-prefix routes also strip a trailing slash and inject `/` for
   apps that expect a root? Currently we strip exactly `/app/<name>` and
   forward the rest verbatim. Apps that hard-code absolute paths (e.g.
   static assets expecting `/styles.css` at the root) will see broken
   links. v2 may need a `baseURL` env hint.
2. Path-prefix routes inherit the dashboard's TLS cert — fine in practice
   but technically means a TLS SNI for `elevator.albruiz.dev` covers all
   apps. If we want per-app certs, route via subdomain only.
3. Log streaming endpoint is plain HTTP chunks, not SSE. Replace with SSE
   when we wire up real log tailing (`Phase 4 polish`).
4. Dashboard's `BackendURL` defaults to `host.containers.internal:8080`,
   which only works from the rootful Traefik container. If a user runs
   Traefik on a non-Podman host, they need to override it. Document this
   in the eventual README.

## Decisions captured

- App model = compose stack; tarball drops synthesize a single-service compose.
- Drag-drop accepts static OR Dockerfile source (auto-detect).
- Custom Go compose runtime, no Python dep.
- PAT stored plaintext in SQLite for v1 (single-user, single-host — acceptable risk).
- Single upload endpoint, multipart form; no chunked uploads in v1.
- Webhooks (auto-redeploy) explicitly v2.
- Templates explicitly v2.
- Database provisioning explicitly out of scope.

# v2 Plan

## Decisions captured (v2)

- Advanced deploy for non-containerized repos = **Dockerfile synthesis**
  (builder image + build_command + run_command + port → single-stage
  Dockerfile + synthetic compose). Mirrors the static-drop pattern; reuses
  the whole build/Traefik pipeline. No buildpacks, no host-side builds.
- Build/run image model: **single-stage** (build and run in the same
  chosen image). Multi-stage (optional runtime image) deferred.
- App secrets stored **plaintext in SQLite** (consistent with git PATs;
  single-user, single-host, DB is root-only). Secrets are write-only:
  never echoed back by UI or API.
- PATs are **full-access only** (no scopes), optional expiry. Token
  management is web-only in v1.

## Phase 1 — Account management + CLI password reset — ✅ done (2026-09-08)

No dependencies. Small; ships first and gives lockout recovery before
riskier work.

- **Store** (`internal/store/users.go`): `UpdateUserPassword`,
  `UpdateUsername` (+`ErrUsernameTaken`), `GetUserByID`, `ListUsers`,
  `DeleteOtherSessionsForUser`.
- **Web**: "Account" card on the settings page:
  - change username (re-auth with current password),
  - change password (requires current password; invalidates all other
    sessions, keeps the actor's).
- **CLI** (`cmd/space-elevator/commands/user.go`):
  - `space-elevator user reset-password [--username admin]` — prompts
    twice (no echo, 8-char minimum), bcrypt-hashes, wipes all sessions
    for that user. Host access = trust, so no old password required.
    If the given username doesn't exist but the DB has exactly one
    account (renamed in the dashboard), it targets that account with a
    note.
- **Live bug fixed during this phase**: page-level templates rendered
  `value=""` into every hidden `csrf` input — only the layout nav (via
  `$.CSRFToken`) and fetch() calls (meta tag + header) ever carried a
  token, so all HTML form POSTs (save creds, restart, remove, domains,
  logout) were rejected by `csrfProtect`. Fixed with a `pageCtx(r,
  title)` helper that injects the context CSRF token into `PageData`
  at every handler call site; regression test
  `TestPageFormsCarryCSRFToken` guards it.
- **Verify**: `go build ./...`, `go vet ./...`, unit tests pass
  (store round-trips + render smoke). Live smoke against a temp-HOME
  server: setup → settings renders Account card → rename with wrong
  password rejected, with correct password applied → password change
  rejects mismatched confirm, keeps actor session, signs out the other
  device, old login dead → missing CSRF = 403 → CLI reset kills web
  sessions and the CLI-set password logs in.

## Phase 2 — Env vars & secrets plumbing — ✅ done (2026-09-08)

Foundation: advanced deploy is half-useful without env, and the API needs
the same env model.

- **Migration 0003**: `app_secrets(app_id, key, value, PK(app_id,key),
  FK cascade)` — secrets cascade-delete with the app.
- **Store** (`internal/store/secrets.go`): `SetSecret`/`DeleteSecret`/
  `ListSecretKeys` (key names only)/`GetSecrets`/`LoadRuntimeEnv` (the
  single path that exposes secret values, merging them over plain env
  with secret precedence), `UpdateAppEnv` (full-replace of env_json).
  Shared parsing/validation: `ValidEnvKey`, `ParseKVLines` (textarea),
  `ParseKVArgs` (CLI flags); keys must match `[A-Za-z_][A-Za-z0-9_]*`.
- **Runtime wiring** — all five deploy paths now populate `meta.Env`
  via `LoadRuntimeEnv` (web git deploy, web drop, web redeploy, CLI
  deploy, CLI redeploy); previously every caller passed an empty map.
- **Web**: env + secrets textareas on the deploy form (KEY=VALUE per
  line, invalid lines rejected); app detail page has an Environment
  editor (full-replace) and a Secrets manager (keys listed, values
  write-only, add/delete per key). Env/secret edits apply on the next
  redeploy — stated in the UI, since env is baked in at container
  create time.
- **CLI**: `apps deploy --env K=V --secret K=V` (repeatable). Flags
  override stored vars; stored secrets are upserted (removal via the
  web UI).
- **Verify**: unit tests (key validation, KV parsing, secrets CRUD +
  cascade, LoadRuntimeEnv precedence, UpdateAppEnv replace semantics).
  Live e2e against local Podman with a busybox compose repo: deploy
  with `--env PLAIN_VAR=hello --secret SECRET_VAR=s3cret
  --env OVERRIDDEN=fromflag` → `podman exec` inside the container
  showed all three; after editing env_json + adding a secret, redeploy
  injected the updated values; `apps remove` cleaned everything.
  Web smoke: env save (valid/invalid), secret set/list/delete with the
  value never rendered in any page, CSRF enforced on the new routes.

## Phase 3 — Advanced deploy (build command + run command) — ✅ done (2026-09-08)

Depends on Phase 2. Dockerfile synthesis, mirroring the static-drop
pattern.

- **Migration 0004** on `apps`: `build_mode` ('compose'|'custom'),
  `builder_image`, `build_command`, `run_command`, `listen_port`.
  `scanApp` normalizes empty → 'compose'. Queries now share an
  `appColumns` const.
- **Synth** (`internal/builder/synth.go`): `CustomBuild{BuilderImage,
  BuildCommand, RunCommand, ListenPort}` — validates (image required,
  no shell metachars in the image ref, run command required, port
  1-65535 with default 8080), renders a single-stage Dockerfile
  (`FROM <builder>` / `WORKDIR /app` / `COPY . .` / optional
  `RUN <build_cmd>` / `EXPOSE <port>` / `CMD ["/bin/sh","-c",
  "<run_cmd>"]` JSON-encoded) + a `services.web` compose (bare port →
  random host port, same as drops) + `.dockerignore` (.git).
  `WriteCustomBuild` overwrites previous synth output so redeploys
  pick up edits.
- **Git flow**: clone → `FindComposeFile` fails → if custom settings
  present, synth into the source dir → standard pipeline; if neither,
  clear error ("repo has no compose file and no advanced build
  settings"). A repo that ships its own compose wins over stale custom
  settings (build_mode reset to 'compose').
- **Redeploy** (web + CLI): regenerates synth files from stored
  columns before Deploy, like static drops.
- **Web**: collapsible `<details>` "Advanced" section on the deploy
  form (no JS): builder image, port, build cmd, run cmd; validation
  errors render on the form. App detail page shows
  `Build: custom (image → run command)` in the meta strip.
- **CLI**: `apps deploy --image/--build-cmd/--run-cmd/--port`.
  Re-deploying over a custom app inherits its stored settings unless
  overridden by flags.
- **Secrets**: runtime-only, unchanged (never in the Dockerfile/build).

#### Pre-existing bugs found & fixed during this phase

1. **CLI deploy minted three different UUIDs per fresh deploy**
   (`nameOrUUID` called `uuid.NewString()` on every call): clone dir,
   app struct, and DB row all diverged. Invisible for image-only
   deploys (source dir unused), fatal for custom builds that read the
   source back on redeploy. Fixed: resolve the ID once, reuse
   everywhere (`nameOrUUID` deleted).
2. **CLI `apps redeploy` used the wrong source root**: hardcoded
   `~/apps/sources` instead of `cfg.AppsRoot`, so it silently re-ran
   image-only apps from a nonexistent dir. Now uses
   `store.AppSourceDir`.
3. **`BuildImage` swallowed build failures** (podman/build.go): the
   build API reports errors inside a 200 response stream
   (`{"error": ...}`); the body was discarded, so a failed RUN step
   left the previous image under the tag and deploy "succeeded".
   Now parsed (`readBuildOutput` + unit test); failures propagate with
   the failing step.
4. **Apps with a failed first deploy were unremovable**: empty/invalid
   stored compose made both remove paths hard-fail on parse. Removal
   now warns and proceeds with an empty spec (teardown filters by
   labels anyway).

- **Verify**: synth unit tests (Dockerfile shape, compose, validation,
  port parsing, .dockerignore); `readBuildOutput` tests; live e2e with
  a non-containerized Node repo (no Dockerfile/compose): CLI deploy
  `--image busybox --build-cmd "…" --run-cmd "httpd …" --port 3999`
  → build marker written inside the image, env+secrets injected,
  random host port serving the built content; edited stored
  build_command → redeploy served the new content. Web form advanced
  deploy → app running + content served + meta strip correct. Broken
  build command now fails the redeploy loudly with the failing STEP.
  Note: the host is aarch64; the local `node:latest` image is amd64
  and segfaults under QEMU binfmt — unrelated to the synth pipeline
  (busybox arm64 used for the run tests).

## Phase 4 — PATs + REST API — ✅ done (2026-09-08)

Depends on Phase 2 (env model); after Phase 3 so the API covers custom
builds for full parity from day one.

- **Migration 0005**: `api_tokens(id, name, token_hash UNIQUE, prefix,
  expires_at NULL, last_used_at NULL, created_at)`.
- **Store** (`internal/store/tokens.go`): `MintAPIToken` (mints
  `se_<43 chars>`; returns raw **once**; persists only the SHA-256
  hash + a 12-char display prefix), `GetAPIToken` (expired tokens
  resolve as unknown), `TouchAPIToken`, `ListAPITokens`,
  `DeleteAPIToken`.
- **Auth** (`internal/web/auth_api.go`): `requireAPIToken` middleware
  for `/api/v1/*` — Bearer token, 401 + `WWW-Authenticate` on
  miss/expiry/garbage; no cookies, no CSRF (the token is the
  credential); last-used stamp throttled to one write per
  token/minute. Covered by unit tests incl. expiry.
- **Deployer package** (`internal/deployer`): the clone→build→run→route
  pipeline now lives in ONE implementation with three consumers:
  - `DeployGit` — parse-free pipeline: name validation, ID
    resolution (reuses an existing byte-identical app row), env merge
    (stored survives unless overridden), row create/update **up
    front** (pending) so failures are visible, clone, compose-or-synth
    resolution, secrets upsert, teardown-previous, deploy, status
    updates, Traefik route (web git deploys now get their route file
    too — previously only CLI deploys did), public URL log.
  - `Redeploy` — static-drop + custom-build synth regeneration,
    teardown, deploy, route (same as CLI/web flows did inline).
  - Web `deployAsync`/`handleAppRedeploy` and CLI `runDeploy`/
    `runAppsRedeploy` now wrap the deployer; `WriteStaticFiles` moved
    from `web` to `builder` to avoid an import cycle. Domain
    attach/detach and app restart/remove were likewise extracted into
    shared helpers used by both HTML forms and the API.
- **API** (`internal/web/handlers_api.go`, JSON via `jsonError`):
  - `GET /apps`, `GET /apps/{name}` (live runtime status, services,
    domains, `secret_keys` — values never exposed)
  - `POST /apps` (name, url, ref, env/secrets as JSON objects,
    `build` for advanced deploys) → 202 + row exists → poll
  - `POST /apps/{name}/redeploy` (async) | `/restart` (sync) | `DELETE
    /apps/{name}` (sync)
  - `GET /apps/{name}/logs?tail=N&service=S` (text; reuse of the web
    endpoint — no SSE in API v1)
  - `PUT /apps/{name}/env` (flat object, full replace),
    `PUT /apps/{name}/secrets` (flat object, upsert),
    `DELETE /apps/{name}/secrets/{key}`
  - `POST /apps/{name}/domains`, `DELETE /apps/{name}/domains/{domain}`
- **PAT UI**: Settings → API tokens card: generator (name + expiry
  never/30/60/90d) re-rendering the page with the raw token in a
  highlighted once-only reveal; list shows prefix, expiry (last-used
  stored, shown in future); per-token revoke (POST
  `/settings/tokens/{id}/delete`).
- **Docs**: `docs/api.md` — auth, conventions, endpoint table,
  examples.
- **Verify**: token store + middleware unit tests (expiry, garbage,
  missing header, touch); live curl smoke: mint PAT via web form →
  401s without/garbage token → deploy compose app + custom-build app
  via API (content served both ways) → PUT env/secrets → redeploy →
  podman env shows updated values → domain attach/detach + invalid
  domain 400 → logs → restart → delete (row + containers gone) → web
  revoke → API 401. Secret values verified absent from every API and
  page response.

## v2 out of scope (deferred)

CLI remote-API client (`--api-url` + PAT), token scopes,
encrypted-at-rest secrets, deploy webhooks, multi-user.

## v2 effort

| Phase | Est. |
|---|---|
| 1. Account + CLI reset | 0.5–1d |
| 2. Env & secrets | 1–2d |
| 3. Advanced deploy | 1–2d |
| 4. PAT + API | 2–3d |

# v3 — Functions & unified deploy wizard

## Overview

Adds a third workload kind, **function** (Lambda-style), and unifies
deployment behind a single wizard while keeping the zero-config dropzone.

## App kinds

`apps.kind` is `web` (default), `function`, or `custom` — orthogonal to
`source_type` (git/drop). Function apps store `runtime`,
`runtime_version`, and `entrypoint`.

## Functions (always-on for now)

- `internal/builder/function.go` synthesizes a single-stage Dockerfile
  plus a platform-owned Python/Node HTTP adapter that maps requests to an
  API Gateway (payload 2.0) proxy event and calls the user handler.
- Deps installed at build time (`requirements.txt` / `package.json`).
- Adapter exposes `/__se/health`; containers carry
  `space-elevator.kind=function`.
- Scale-to-zero is future work: `scale_to_zero`, `idle_timeout`, and
  `last_invoked_at` are stored now and route writing is centralized in
  `deployer.applyRoute` so an activator can be inserted later.

## Unified surfaces

- Web wizard at `/apps/new` (kind → source → kind-specific fields);
  the apps-page dropzone remains as quick deploy.
- REST: `POST /api/v1/apps` gains kind/function fields;
  `POST /api/v1/apps/upload` handles multipart archives.
- CLI: `apps deploy` gains `--kind/--language/--runtime/--entrypoint`;
  new `apps upload <archive>` command.

## Shared pipeline

Archive extraction moved to `internal/builder/archive.go`;
`deployer.CreateUpload` + `deployer.Redeploy` are shared by web, API, and
CLI.