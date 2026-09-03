# Plan: space-elevator (Podman Deployment Tool)

## Overview

A Podman-first deployment tool written in Go. Single binary that serves both as a CLI and a web dashboard. Auto-generates Traefik dynamic configs and manages containers via Podman's Docker-compatible REST API.

## Goals

- Podman-native (no Docker dependency)
- Simple web UI to deploy/manage apps
- CLI for headless/server workflows
- Traefik integration (file provider, auto-generated dynamic YAML)
- One-click app templates (WordPress, Gitea, Uptime Kuma, etc.)
- Survives reboots (quadlet generation)

## Architecture

```
┌─────────────────────────────────────────────────┐
│              space-elevator (Go binary)          │
├──────────┬──────────┬───────────┬───────────────┤
│   CLI    │  Web UI  │  Podman   │   Traefik     │
│ commands │ (embed)  │  client   │   config gen  │
├──────────┴──────────┴───────────┴───────────────┤
│              SQLite (state/store)                │
├─────────────────────────────────────────────────┤
│ /run/podman/podman.sock  │  Traefik dynamic dir  │
└─────────────────────────────────────────────────┘
```

## Tech Stack

| Component | Choice | Rationale |
|---|---|---|
| Language | Go 1.22+ | Single binary, great Podman libs |
| Podman client | `github.com/docker/docker/client` | Podman socket is Docker-API compatible |
| CLI framework | `cobra` | Standard Go CLI framework |
| State storage | SQLite (`modernc.org/sqlite`) | Pure Go, no CGo |
| Web framework | `net/http` + `chi` router | Lightweight, stdlib-friendly |
| Frontend | HTML + HTMX | No build step, embeddable |
| Templates | YAML definitions | Simple, readable |
| Container persistence | Quadlet generation | Matches existing systemd pattern |

## Directory Structure

```
~/Projects/space-elevator/
├── PLAN.md                          # This file
├── README.md                        # Project intro
├── Makefile                         # Build automation
├── go.mod
├── go.sum
├── cmd/
│   └── space-elevator/
│       └── main.go                  # CLI + server entry point
├── internal/
│   ├── podman/                      # Podman API client
│   │   ├── client.go
│   │   ├── containers.go
│   │   ├── images.go
│   │   └── networks.go
│   ├── traefik/                     # Traefik config generator
│   │   ├── config.go
│   │   └── writer.go
│   ├── builder/                     # Git clone + image build
│   │   ├── git.go
│   │   └── build.go
│   ├── deployer/                    # Container lifecycle
│   │   ├── deploy.go
│   │   └── lifecycle.go
│   ├── store/                       # SQLite state
│   │   ├── store.go
│   │   └── models.go
│   ├── templates/                   # Template engine
│   │   └── templates.go
│   ├── config/                      # App config (paths, defaults)
│   │   └── config.go
│   └── api/                         # REST API + web handlers
│       ├── router.go
│       ├── handlers.go
│       └── middleware.go
├── web/                             # Embedded frontend
│   ├── static/
│   │   ├── css/
│   │   ├── js/
│   │   └── img/
│   └── pages/                       # HTML templates
├── templates/                       # App template YAML files
│   ├── wordpress.yml
│   ├── gitea.yml
│   ├── uptime-kuma.yml
│   └── registry.yml
└── docs/
    ├── architecture.md
    └── deployment.md
```

## Phased Implementation

### Phase 1: Scaffolding + Podman Client
**Model**: MiniMax M3 (default workhorse)
- Initialize Go module
- Implement Podman client (containers, images, networks)
- Basic CLI: `space-elevator ps`, `space-elevator images`, `space-elevator info`
- Config module (paths, defaults)

### Phase 2: Traefik Integration
**Model**: MiniMax M3
- Generate per-app dynamic YAML files (router + service + TLS)
- Write to `/etc/space-elevator/traefik/dynamic/`
- CLI: `space-elevator domain add <app> <domain>`, `space-elevator domain list`

### Phase 3: Git Deployments
**Model**: MiniMax M3
- Git clone → detect build method → `podman build` → deploy
- App state in SQLite
- CLI: `space-elevator deploy <git-url>`, `space-elevator logs <app>`, `space-elevator remove <app>`

### Phase 4: Web Dashboard
**Model**: GLM-5.3-Flash (HTML scaffolding), MiniMax M3 (API)
- Embedded static assets via `embed`
- REST API backing CLI + web UI
- Pages: Dashboard, Deploy, App Detail, Templates
- Simple password auth

### Phase 5: Templates
**Model**: GLM-5.3-Flash (YAML defs), MiniMax M3 (schema design)
- YAML-based template definitions
- Built-in templates: WordPress, Gitea, Uptime Kuma, Plausible
- CLI: `space-elevator template list`, `space-elevator template deploy <name>`

### Phase 6: Systemd + Persistence
**Model**: MiniMax M3
- Generate quadlet files for deployments
- `space-elevator init` setup
- `space-elevator update` for zero-downtime updates

## Model Selection Strategy

| Model | Tier | Use For |
|---|---|---|
| **Kimi K3** | Premium reasoning | Architecture decisions, complex bugs, deep review (sparingly — 5-8 invocations total) |
| **MiniMax M3** | Balanced | Most implementation work |
| **GLM-5.3-Flash** | Fast/cheap | Boilerplate, YAML configs, HTML scaffolding |
| **Qwen 3.8-Flash** | Fast/cheap | Quick fixes, simple edits |

### Kimi K3 Budget

With "very few uses" available, reserve for:
1. Phase 1 → Final architecture review
2. Phase 2 → Traefik edge cases (TLS/routing gotchas)
3. Phase 3 → Build optimization strategy
4. Phase 4 → Auth/security model review
5. Phase 6 → Final integration review

## Traefik Integration Plan

Add a second file provider to existing `/etc/containers/systemd/traefik/traefik.yml`:

```yaml
providers:
  file:
    directory: /etc/containers/systemd/traefik/dynamic
  file.space-elevator:
    directory: /etc/space-elevator/traefik/dynamic
```

Each deployed app gets a YAML file like:

```yaml
http:
  routers:
    myapp-web:
      rule: "Host(`myapp.example.com`)"
      entryPoints: [web]
      service: myapp
    myapp-secure:
      rule: "Host(`myapp.example.com`)"
      entryPoints: [websecure]
      service: myapp
      tls:
        certResolver: letsencrypt
  services:
    myapp:
      loadBalancer:
        servers:
          - url: "http://10.89.0.XX:PORT"
```

## Estimated Total Effort

| Phase | Days | Status |
|---|---|---|
| 1. Scaffolding + Podman | 2-3 | Pending |
| 2. Traefik Integration | 1-2 | Pending |
| 3. Git Deployments | 2-3 | Pending |
| 4. Web Dashboard | 3-4 | Pending |
| 5. Templates | 1-2 | Pending |
| 6. Systemd + Persistence | 1-2 | Pending |

**Total**: ~10-16 days for functional v1.
