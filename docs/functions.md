# Writing functions

A **function** app is a small Python or Node program that space-elevator
wraps in a generated HTTP server (the *adapter*) and runs as an always-on
container behind Traefik. You write a single handler; the platform builds
the image, installs dependencies, routes traffic, and injects env/secrets.

Deploy one from the dashboard (**New app → Function**), the API
(`POST /api/v1/apps/upload` or `POST /api/v1/apps` with `"kind":
"function"`), or the CLI:

```sh
space-elevator apps upload fn.zip --kind function \
  --language python --runtime 3.12 --entrypoint handler.py:handler
```

## Entrypoint

The entrypoint is `file:export`. Defaults by language:

| Language | Default entrypoint  | Meaning                       |
|----------|---------------------|-------------------------------|
| `python` | `handler.py:handler`| `handler` in `handler.py`     |
| `node`   | `index.js:handler`  | `handler` export in `index.js`|

`file` may be a relative path (`app/main.py`) and `export` must be a
valid identifier. The file is resolved inside the deployed source root.

## Handler signature

Python:

```python
def handler(event, context):
    return {"ok": True}
```

Node (CommonJS or ESM all work):

```js
exports.handler = async (event, context) => ({ ok: true });
// or: export const handler = async (event, context) => ({ ok: true });
// or: module.exports = async (event, context) => ({ ok: true });
```

## The event

Every HTTP request becomes an API Gateway-style proxy event. Payload 2.0
fields are present, plus payload 1.0 aliases (`httpMethod`, `path`) so
either convention works.

```json
{
  "version": "2.0",
  "routeKey": "$default",
  "rawPath": "/hello",
  "rawQueryString": "name=ada",
  "httpMethod": "GET",
  "path": "/hello",
  "headers": { "host": "…", "user-agent": "curl/8" },
  "queryStringParameters": { "name": "ada" },
  "body": null,
  "isBase64Encoded": false,
  "requestContext": {
    "http": { "method": "GET", "path": "/hello", "protocol": "HTTP/1.1", "sourceIp": "…" },
    "requestId": "…",
    "stage": "$default"
  }
}
```

Notes:
- `body` is the raw request body string, `null` when empty. Binary bodies
  are base64-encoded with `isBase64Encoded: true`.
- Duplicate query parameters are grouped into a list.
- For `POST`, parse `body` yourself (e.g. `json.loads(event["body"])` /
  `JSON.parse(event.body)`) and check the `content-type` header.

## The response

Return either a Lambda-proxy response object or any JSON-serializable
value:

```python
# Full control:
return {
    "statusCode": 201,
    "headers": {"content-type": "application/json"},
    "body": json.dumps({"id": 1}),
}
# `body` may also be a dict/list (auto-serialized).
# `cookies`: ["session=…"] becomes Set-Cookie headers.
# `isBase64Encoded`: true means `body` is base64 and decoded before sending.

# Or just return a value → 200 application/json:
return {"id": 1}
```

Node versions use the same keys (`statusCode`, `headers`, `body`,
`cookies`, `isBase64Encoded`) with camelCase handled identically.

Uncaught exceptions return `500` with
`{"errorMessage": "...", "errorType": "..."}` and are written to the
container logs (visible on the app page).

## Context

The second argument carries minimal Lambda-like metadata:

| Field | Python / Node |
|---|---|
| request id | `context.aws_request_id` / `context.awsRequestId` |
| function name | `context.function_name` / `context.functionName` |
| memory (MB) | `context.memory_limit_in_mb` / `context.memoryLimitInMB` |
| remaining ms | `context.get_remaining_time_in_millis()` / `context.getRemainingTimeInMillis()` |
| log group/stream | `log_group_name` / `logGroupName`, `log_stream_name` / `logStreamName` |
| ARN | `invoked_function_arn` / `invokedFunctionArn` (locally synthetic) |

## Dependencies

Installed at image build time, so cold starts stay fast:

- **Python**: a `requirements.txt` at the source root is installed with
  `pip install --no-cache-dir -r requirements.txt`.
- **Node**: a `package.json` runs `npm ci || npm install`.

Everything else uses the standard library / base image.

## Environment and secrets

Plain env vars are injected at runtime and are also available at build
time (as Dockerfile `ARG`s). Secrets are runtime-only and never appear in
build layers. In Python use `os.environ[...]`; in Node `process.env.…`.

## Routing and health

The function is reachable at the app's normal path prefix
(`/<prefix>/<name>-web/`) or any attached domain. The adapter also
exposes `GET /__se/health` → `200 {"status":"ok"}` once the handler loads
(`503` while loading or on a load error). Container labels include
`space-elevator.kind=function`; `scale_to_zero`/`idle_timeout` are
recorded now for a future activator, but functions run always-on today —
**keep them stateless**, since a restart loses in-memory state.

## Test the adapter locally

The adapter is a standalone file; point it at your source and run it.

```sh
# Python
SE_FUNCTION_ENTRYPOINT=handler.py:handler \
SE_FUNCTION_APPDIR="$PWD" \
PORT=8080 python3 /opt/function/server.py   # path inside the image
```

```sh
# Node
SE_FUNCTION_ENTRYPOINT=index.js:handler \
SE_FUNCTION_APPDIR="$PWD" \
PORT=8080 node /opt/function/server.mjs
```

Inside a deployed container the adapters live at
`/opt/function/server.py` and `/opt/function/server.mjs`. For local
development you can copy the template from
`internal/builder/function_templates/` and run it the same way. The
templates are stdlib-only, so no dependencies are needed to exercise them:

```sh
curl -s localhost:8080/__se/health
curl -s 'localhost:8080/hello?name=ada'
```

## Complete example

`handler.py`:

```python
import json
import os

def handler(event, context):
    if event["httpMethod"] != "POST":
        return {"statusCode": 405, "body": "use POST"}
    payload = json.loads(event.get("body") or "{}")
    greeting = os.environ.get("GREETING", "hello")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json"},
        "body": json.dumps({"message": f"{greeting}, {payload.get('name', 'world')}"}),
    }
```

`requirements.txt` (optional):

```
# requests==2.32.0
```

`index.js`:

```js
exports.handler = async (event, context) => {
  const body = JSON.parse(event.body || '{}');
  const greeting = process.env.GREETING || 'hello';
  return {
    statusCode: 200,
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ message: `${greeting}, ${body.name || 'world'}` }),
  };
};
```
