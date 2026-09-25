# space-elevator REST API (v1)

Single-user, full-access REST API mirroring the dashboard. Authenticated
with API tokens issued and managed by the external Token-Manager service.
Space-elevator validates each bearer token through Token-Manager; it does not
store or create API tokens locally. The raw token is shown once by
Token-Manager; store it securely.

## Authentication

```
Authorization: Bearer tm_<env>_<token>
```

The token is validated by Token-Manager against the client app configured in
space-elevator. Missing, invalid, expired, or revoked tokens get `401` with a
`WWW-Authenticate: Bearer` challenge. If Token-Manager is unavailable, not
configured, or its client credentials are invalid, requests fail closed with
`503`; space-elevator does not fall back to local tokens. Tokens carry no
scopes and act as full account access; give each integration its own named
token and revoke it in Token-Manager when done.

Create the Token-Manager app named `space-elevator` and configure
`token_manager_url`, `token_manager_client_id`, and
`token_manager_client_secret` in space-elevator. Token generation and
revocation happen in Token-Manager, not in the space-elevator dashboard.

## Conventions

- Base URL: `/api/v1`
- JSON in and out, except `GET /apps/{name}/logs` (plain text).
- Errors: `{"error": "message"}` with a 4xx/5xx status.
- Deploys and redeploys are **asynchronous** (`202 Accepted`); poll the
  app resource until `status` leaves `pending` (success) or becomes
  `error` (the `last_error` field explains why). Deployment responses also
  include `operation_id` and `status_url`.
- Environment/secret changes say so in `note`: they are baked into
  containers at create time and apply on the **next redeploy**.
- Secret values are write-only. They can be set, re-set, and deleted,
  but never read back — only `secret_keys` is exposed.

Endpoints:

### Deployment status

Every asynchronous deploy/update/redeploy returns an `operation_id`. Poll
`GET /api/v1/deployments/{operation_id}` rather than inferring progress from
container state. The response contains the operation status, current step,
ordered deployment steps, and output lines:

```json
{
  "status": "building",
  "label": "Building",
  "current_step": "build",
  "seq": 42,
  "steps": [
    {"key": "build", "label": "Build images", "status": "running"},
    {"key": "activate", "label": "Start candidate release", "status": "pending"}
  ],
  "lines": [
    {"sequence": 42, "step": "build", "level": "info", "message": "pulling base image"}
  ]
}
```

Use `?after=<seq>` to receive only new output lines. Terminal operation
states include `completed`, `failed`, `rolled_back`, and `rollback_failed`.
A rolled-back update leaves the previous release running.

| Method | Path | Description |
|---|---|---|
| GET | `/apps` | List apps (stored status, domains, secret keys) |
| GET | `/apps/{name}` | App detail incl. live runtime status + services |
| POST | `/apps` | Deploy from git (202; see below) |
| POST | `/apps/upload` | Deploy an uploaded archive, multipart (202; see below) |
| POST | `/apps/{name}/redeploy` | Rebuild+recreate from stored source (async) |
| GET | `/deployments/{id}` | Durable deployment status, steps, and recent output |
| POST | `/apps/{name}/update` | Update a Git app to a new ref/commit (async) |
| POST | `/apps/{name}/update/upload` | Replace an archive app source (multipart, async) |
| POST | `/apps/{name}/restart` | Restart containers (env changes do NOT apply) |
| POST | `/apps/{name}/start` | Start stopped containers |
| POST | `/apps/{name}/stop` | Stop containers, keeping them for a later start |
| POST | `/apps/{name}/rename` | Rename app `{"name": "new-name"}` (no rebuild) |
| DELETE | `/apps/{name}` | Remove app (containers, route, artifacts) |
| GET | `/apps/{name}/logs?tail=200&service=web` | Log tail (plain text) |
| PUT | `/apps/{name}/env` | Replace plain env (flat object) |
| PUT | `/apps/{name}/secrets` | Upsert secrets (flat object of KEY→value) |
| DELETE | `/apps/{name}/secrets/{key}` | Delete one secret |
| POST | `/apps/{name}/domains` | Attach domain `{"domain": "app.example.com"}` |
| DELETE | `/apps/{name}/domains/{domain}` | Detach domain |

## Deploying a repo

```
POST /api/v1/apps
{
  "name": "my-api",
  "url": "https://github.com/you/repo.git",
  "ref": "main",
  "env": {"NODE_ENV": "production"},
  "secrets": {"API_TOKEN": "hunter2"},
  "kind": "web",
  "build": {
    "image": "node:20-bookworm",
    "build_command": "npm ci && npm run build",
    "run_command": "npm start",
    "port": 3000
  }
}
→ 202 {"name":"my-api","status":"pending"}
```

- `kind` is `web` (default), `function`, or `custom`. `custom` means
  bring-your-own-container (a compose/Dockerfile or the `build` block);
  a repo that ships its own compose file always wins over `build`.
- `build` is optional: repos that ship `compose.yml` deploy as-is.
  Setting `build` switches to advanced deploy (synthesized single-stage
  Dockerfile). Static Git web apps can use:

  ```json
  "build": {
    "mode": "static",
    "image": "node:20-bookworm",
    "build_command": "npm ci && npm run build",
    "serve_path": "dist",
    "port": 80
  }
  ```

  Static builds use a generated multi-stage image with Nginx. `serve_path`
  is relative to the repository root; omit it or use `"."` to serve the root.
- `name` may be omitted if it can be derived from the repo URL.
- Deploying over an existing name is rejected (`409`); use
  `POST /apps/{name}/update` for a Git app or
  `POST /apps/{name}/update/upload` for an archive app.
- Invalid env/secret key names → `400` (keys must match
  `[A-Za-z_][A-Za-z0-9_]*`).

### Deploying a function from git

```
POST /api/v1/apps
{
  "name": "resize-image",
  "url": "https://github.com/you/functions.git",
  "kind": "function",
  "runtime": "python",
  "runtime_version": "3.12",
  "entrypoint": "handler.py:handler",
  "scale_to_zero": false,
  "idle_timeout": 0
}
→ 202 {"name":"resize-image","status":"pending"}
```

`runtime` is `python` or `node`; `runtime_version` is the base image tag
(default `3.12` / `20`); `entrypoint` is `file:handler` (default
`handler.py:handler` / `index.js:handler`). The platform generates an
HTTP adapter that calls the handler with an API Gateway-style event and
serves its return value. `scale_to_zero`/`idle_timeout` are stored for a
future activator and are currently inert.

See [functions.md](./functions.md) for the handler contract and examples.

## Deploying an archive

```
POST /api/v1/apps/upload        # multipart/form-data
  tarball=<file>                # required; .tar.gz / .tgz / .zip
  kind=function                 # web (default) | function | custom
  name=resize-image
  language=python
  runtime_version=3.12
  entrypoint=handler.py:handler
  env=API_KEY=...               # KEY=VALUE lines, one per newline
  secrets=TOKEN=...
→ 202 {"name":"resize-image","status":"pending"}
```

- `kind=web` auto-detects a static site or a Dockerfile.
- `kind=custom` requires a Dockerfile in the archive.
- `kind=function` requires `language` (and optional `runtime_version` /
  `entrypoint`).
- As with all deploys, poll `GET /apps/{name}` until `status` leaves
  `pending`.

## Updating an app

Update replaces the source release while preserving the app's stable ID, slug,
domains, environment, secrets, and managed persistent storage. Updates run
asynchronously and briefly stop the app while the new containers are activated.
Persistent volumes and writable bind data are backed up first; a failed
startup attempts to restore the previous release.

For a Git app, the repository URL is retained and the ref may be a branch,
tag, or commit SHA:

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"ref":"v2.4"}' \
  $API/apps/my-api/update
```

For an archive app, send a new archive. The source type cannot be changed by
an update:

```sh
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -F tarball=@release-v2.tar.gz \
  $API/apps/my-api/update/upload
```

Both endpoints return `202` with `status: "updating"`. Poll
`GET /apps/{name}` until the app leaves `updating`; `error` includes the
last failure. Keep logical volume names unchanged between releases. Data in
container root filesystems and external services is not managed by the
platform.

A concurrent second update, redeploy, or lifecycle action for the same app is
rejected with `409` while an operation is active; poll until the app is idle
before mutating env, secrets, domains, or build settings.

### Persistent storage model

Compose storage is resolved once per app and recorded in `app_storage`, so a
source replacement cannot silently swap the data directory:

- **Named volumes** are app-scoped: the physical Podman volume is
  `se-vol-<app-id>-<hash>`, never the bare logical name. Two apps can both
  declare `dbdata` without colliding, and one app can never adopt a volume
  labelled for another app.
- **Writable relative binds** (`./data:/data`) are copied once into a managed
  host directory under `<apps_root>/data/<app-id>/…` on the first deploy and
  from then on are mounted from there. The next release sees the data at the
  same physical path instead of a freshly-emptied checkout.
- **Read-only relative binds** stay attached to the release checkout.
- **Absolute host paths** in a compose file are rejected — they would mount
  arbitrary host directories into a container. Use a named volume or a
  relative path.
- Changing a logical entry from a volume to a bind (or vice versa) is refused;
  remove the app's storage mapping explicitly first so data is never
  reinterpreted.


```
POST /api/v1/apps/my-api/rename
{"name": "my-service"}
→ 200 {"name":"my-service","status":"renamed"}
```

- Only the display name and dashboard URL change. Containers, networks,
  images, Traefik routes, and attached domains keep running under the
  app's original runtime identity, so **no rebuild or downtime occurs**.
- Invalid names → `400`; a name already in use → `409`.

## Reading deploy results

```
GET /api/v1/apps/my-api
→ 200
{
  "id": "…", "name": "my-api", "status": "running" | "pending" | "error",
  "runtime_status": "running",          # live; per containers
  "last_error": "",                      # set when status=error
  "env": {"NODE_ENV": "production"},
  "secret_keys": ["API_TOKEN"],
  "domains": [], "services": ["web"], …
}
```

## Example session

```sh
TOKEN=tm_live_xxx   # created in Token-Manager
API=https://elevator.albruiz.dev/api/v1   # or the dashboard origin

curl -s -H "Authorization: Bearer $TOKEN" $API/apps

curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  -d '{"url":"https://github.com/example/static.git","name":"site"}' \
  $API/apps

curl -s -H "Authorization: Bearer $TOKEN" $API/apps/site

curl -s -X PUT -H "Authorization: Bearer $TOKEN" \
  -d '{"CACHE_TTL":"60"}' $API/apps/site/env

curl -s -H "Authorization: Bearer $TOKEN" "$API/apps/site/logs?tail=100"
```
