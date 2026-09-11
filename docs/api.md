# space-elevator REST API (v1)

Single-user, full-access REST API mirroring the dashboard. Authenticated
with Personal Access Tokens generated on the dashboard's Settings page
(Settings → API tokens → Generate token). The raw token is shown **once**
at creation; store it securely.

## Authentication

```
Authorization: Bearer se_<token>
```

Missing/invalid/expired tokens get `401` with a `WWW-Authenticate: Bearer`
challenge. Tokens carry no scopes and act as full account access; give
each integration its own named token and revoke it when done.

Token generation and revocation is web-only in v1.

## Conventions

- Base URL: `/api/v1`
- JSON in and out, except `GET /apps/{name}/logs` (plain text).
- Errors: `{"error": "message"}` with a 4xx/5xx status.
- Deploys and redeploys are **asynchronous** (`202 Accepted`); poll the
  app resource until `status` leaves `pending` (success) or becomes
  `error` (the `last_error` field explains why).
- Environment/secret changes say so in `note`: they are baked into
  containers at create time and apply on the **next redeploy**.
- Secret values are write-only. They can be set, re-set, and deleted,
  but never read back — only `secret_keys` is exposed.

Endpoints:

| Method | Path | Description |
|---|---|---|
| GET | `/apps` | List apps (stored status, domains, secret keys) |
| GET | `/apps/{name}` | App detail incl. live runtime status + services |
| POST | `/apps` | Deploy from git (202; see below) |
| POST | `/apps/{name}/redeploy` | Rebuild+recreate from stored source (async) |
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
  "build": {
    "image": "node:20-bookworm",
    "build_command": "npm ci && npm run build",
    "run_command": "npm start",
    "port": 3000
  }
}
→ 202 {"name":"my-api","status":"pending"}
```

- `build` is optional: repos that ship `compose.yml` deploy as-is.
  Setting `build` switches to advanced deploy (synthesized single-stage
  Dockerfile); a repo that ships its own compose file wins over it.
- `name` may be omitted if it can be derived from the repo URL.
- Deploying over an existing name is rejected (`409`); use
  `redeploy` to refresh, or `DELETE` first.
- Invalid env/secret key names → `400` (keys must match
  `[A-Za-z_][A-Za-z0-9_]*`).

## Renaming an app

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
TOKEN=se_xxx   # from the dashboard
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
