# Configuration

space-elevator is configured by a single YAML file plus `SPACE_ELEVATOR_*`
environment variables. Both are optional — with neither, it runs with neutral
defaults (loopback dashboard, no Traefik integration).

## Precedence

Highest wins:

1. **CLI flags** (`--addr`, `--config`, `setup --host`, …)
2. **Environment variables** (`SPACE_ELEVATOR_*`, listed per key below)
3. **The config file**
4. **Built-in defaults**

Every command accepts `--config <path>`. Without it, the file is read from:

```
$SPACE_ELEVATOR_CONFIG
$XDG_CONFIG_HOME/space-elevator/config.yaml   # if XDG_CONFIG_HOME is set
~/.config/space-elevator/config.yaml
```

Generate a fully-commented file with `space-elevator config init` (add
`--from-legacy` to seed from an existing install), then edit it freely. Inspect
what is actually in effect with `space-elevator config show`, and check it with
`space-elevator config validate`.

## Exposure modes

`space-elevator setup` asks for one of three mental models:

| Mode | What it means | Sets |
|---|---|---|
| `traefik` | Rootful Traefik terminates TLS and routes `public_host` + `/app/<name>/` | `public_host`, `traefik_dir`, `cert_resolver`, `rootless_gateway`, `bind_addr: 0.0.0.0:8080` |
| `direct` | No proxy; reach apps on their published host ports | clears `public_host`, `traefik_dir`, `cert_resolver`; `bind_addr: 127.0.0.1:8080` |
| `proxy` | An existing reverse proxy fronts the dashboard over HTTP | `public_host`, `traefik_dir`; empty `cert_resolver` (HTTP-only routes) |

## Key reference

### Core

| Key | Env | Default | Notes |
|---|---|---|---|
| `bind_addr` | `SPACE_ELEVATOR_BIND_ADDR` | `127.0.0.1:8080` | Dashboard listen address. Use `0.0.0.0:8080` when a rootful Traefik container must reach it through the host. |
| `socket_path` | `PODMAN_SOCKET` | auto-detect | Podman/Docker API socket. Auto-detect checks `$XDG_RUNTIME_DIR/podman/podman.sock`, `/run/podman/podman.sock` and `/tmp/podman/podman.sock`. |
| `data_dir` | `SPACE_ELEVATOR_DATA_DIR` | `~/.local/share/space-elevator` | Long-lived data (uploads, checkouts). |
| `state_dir` | `SPACE_ELEVATOR_STATE_DIR` | `~/.local/state/space-elevator` | SQLite DB and CSRF key. Created `0700`. |
| `apps_root` | `SPACE_ELEVATOR_APPS_ROOT` | `~/apps` | Where app drops and git checkouts live. |
| `quadlet_dir` | `SPACE_ELEVATOR_QUADLET_DIR` | `~/.config/containers/systemd` | Reserved for quadlet support. |
| `default_network` | `SPACE_ELEVATOR_DEFAULT_NETWORK` | `space-elevator` | Podman network apps attach to. |

### Routing / Traefik

| Key | Env | Default | Notes |
|---|---|---|---|
| `public_host` | `SPACE_ELEVATOR_PUBLIC_HOST` | *(empty)* | Hostname for the dashboard and `/app/<name>/`. Empty disables path-prefix routes and the dashboard self-route. Needs a wildcard DNS record (or per-app records) pointing here. |
| `public_path` | `SPACE_ELEVATOR_PUBLIC_PATH` | *(empty)* | Optional sub-path for the dashboard route, e.g. `/space-elevator/`. |
| `traefik_dir` | `SPACE_ELEVATOR_TRAEFIK_DIR` | *(empty)* | Directory watched by a Traefik **file provider**. Empty disables all Traefik integration. |
| `cert_resolver` | `SPACE_ELEVATOR_CERT_RESOLVER` | *(empty)* | Traefik TLS certificate resolver name. Empty = HTTP-only routes (no TLS, no redirect). |
| `dashboard_url` | `SPACE_ELEVATOR_DASHBOARD_URL` | derived | Backend URL Traefik forwards dashboard traffic to. When empty it is derived: `http://host.containers.internal:<port>` if `traefik_dir` is set, else `http://127.0.0.1:<port>`. |
| `app_path_prefix` | `SPACE_ELEVATOR_APP_PATH_PREFIX` | `/app/` | Path prefix for app routes under `public_host`. |
| `rootless_gateway` | `SPACE_ELEVATOR_ROOTLESS_GATEWAY` | *(empty)* | IP a rootful Traefik uses to reach host-published ports of rootless containers — normally the rootless bridge gateway (e.g. `10.89.0.1`). Empty falls back to the container IP (only works on a shared network). `setup`/`doctor` can detect it. |

### Container defaults

| Key | Env | Default | Notes |
|---|---|---|---|
| `memory_limit` | `SPACE_ELEVATOR_MEMORY_LIMIT` | `512M` | Per-container memory limit; `0` = unlimited. Accepts `K`/`M`/`G` suffixes. |
| `pids_limit` | `SPACE_ELEVATOR_PIDS_LIMIT` | `256` | Per-container PID limit; `0` = unlimited. |
| `insecure_cookies` | `SPACE_ELEVATOR_INSECURE_COOKIES` | `false` | Disables the session cookie `Secure` flag. Plain-HTTP local testing only. |

## Example: public HTTPS with Traefik

```yaml
bind_addr: "0.0.0.0:8080"
public_host: "elevator.example.com"
traefik_dir: "/etc/traefik/dynamic"
cert_resolver: "letsencrypt"
rootless_gateway: "10.89.0.1"
apps_root: "/srv/space-elevator/apps"
```

## Example: direct ports (no proxy)

```yaml
bind_addr: "127.0.0.1:8080"
# public_host, traefik_dir and cert_resolver stay empty
apps_root: "/srv/space-elevator/apps"
```

Apps are reachable at `http://<host>:<published-port>/`. The port is the host
port Podman assigned; see the app detail page or `space-elevator apps list`.

## Minimal Traefik reference

space-elevator only writes **dynamic** config; Traefik's **static** config must
declare matching entrypoints, a file provider, and (optionally) a resolver:

```yaml
entryPoints:
  web:
    address: ":80"
  websecure:
    address: ":443"

providers:
  file:
    directory: /etc/traefik/dynamic      # must equal traefik_dir
    watch: true

certificatesResolvers:
  letsencrypt:                           # must equal cert_resolver
    acme:
      email: you@example.com
      storage: /etc/traefik/acme.json
      tlsChallenge: {}
```

`setup --mode traefik` writes routes that reference the `web`/`websecure`
entrypoints. With `cert_resolver` empty it writes a single HTTP router instead.

## Running as a service

```sh
space-elevator service install            # write unit + enable + start
space-elevator service install --linger   # also start at boot (loginctl enable-linger)
space-elevator service status | restart | logs -f
space-elevator service uninstall          # keeps data
```

The unit is generated from the resolved `bind_addr` and any optional
`~/.config/space-elevator/env` file (systemd `EnvironmentFile`). Put secret-ish
overrides there, e.g.:

```
SPACE_ELEVATOR_PUBLIC_HOST=elevator.example.com
SPACE_ELEVATOR_TRAEFIK_DIR=/etc/traefik/dynamic
SPACE_ELEVATOR_CERT_RESOLVER=letsencrypt
```

## Verifying and migrating

```sh
space-elevator doctor          # Podman, config, Traefik, DB, DNS, systemd — with hints
space-elevator config show     # effective values and env overrides
```

If you are upgrading from a pre-config-file install (where the defaults were
hard-coded to a specific host), generate a config that preserves the existing
routes **before** restarting the service:

```sh
space-elevator config init --from-legacy   # recovers public_host, resolver, gateway
space-elevator service restart
space-elevator doctor
```
