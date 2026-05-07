# MagicForm deployment

Operator-facing setup for running the magicform fork of picoclaw with multi-tenant isolation. Two paths covered:

- **Local native binary** for development on a single machine.
- **Docker compose** for production-like deployment.

Both share the same configuration model and tenant directory layout — see [docs/magicform-integration.md](../docs/magicform-integration.md) for the protocol-level details.

> **What this directory contains**
>
> | File | Purpose |
> |---|---|
> | [`README.md`](./README.md) | This guide |
> | [`docker-compose.magicform.yml`](./docker-compose.magicform.yml) | Compose file for the gateway (multi-tenant ready) |
> | [`config.example.json`](./config.example.json) | Gateway base config template |
> | [`tenant.example.config.json`](./tenant.example.config.json) | Per-tenant overlay template |
> | [`smoke-test.sh`](./smoke-test.sh) | End-to-end test script |

---

## Prerequisites

- Go 1.25+ (for the local path) **or** Docker 20.10+ with compose v2 (for the Docker path).
- An LLM provider API key (Anthropic / OpenAI / etc.).
- A directory the gateway can own as `workspace_root` (we use `/data/workspaces` in examples; pick anything outside the gateway's source tree).

The fork builds with the `goolm,stdjson` build tags so it doesn't need `libolm` C dependencies. The Makefile and the upstream Dockerfile both default to those tags — you don't need to set anything special.

---

## Path A: Local (native binary)

### 1. Build

```bash
cd /c/src/picoclaw
make build
# → produces build/picoclaw (symlinked to build/picoclaw-linux-amd64 etc.)
```

### 2. Lay out tenant directories

```bash
sudo mkdir -p /data/workspaces
sudo chown $USER /data/workspaces
mkdir -p /data/workspaces/default                       # gateway's own (no-tenant) workspace
mkdir -p /data/workspaces/tenant-acme/{workspace,config}
```

### 3. Write the gateway base config

```bash
mkdir -p ~/.picoclaw
cp deploy/config.example.json ~/.picoclaw/config.json
$EDITOR ~/.picoclaw/config.json   # set workspace_root, model_list, channel_list as needed
```

### 4. Write the tenant overlay

```bash
cp deploy/tenant.example.config.json /data/workspaces/tenant-acme/config/config.json
$EDITOR /data/workspaces/tenant-acme/config/config.json   # set the tenant's API key + model
```

Optionally seed bootstrap files (the agent loop will copy them into `workspace/` on first turn):

```bash
echo "You are Acme's customer support assistant." \
  > /data/workspaces/tenant-acme/config/AGENT.md
```

### 5. Set secrets via env

The MagicForm shared secret is a `SecureString`; set it via env, not config.json:

```bash
export PICOCLAW_CHANNELS_MAGICFORM_TOKEN="dev-shared-secret"
export ANTHROPIC_API_KEY="sk-ant-..."
```

### 6. Run

```bash
./build/picoclaw gateway -d
# → "Listening host=127.0.0.1 port=18790"
```

### 7. Smoke test

```bash
./deploy/smoke-test.sh
```

The script: builds (if needed), starts the gateway, exercises the CLI tenant flow, exercises the webhook, and verifies that two distinct tenants ended up with isolated `sessions/` directories.

---

## Path B: Docker

### 1. Build the image

The upstream `docker/Dockerfile` already uses our build tags. Build it:

```bash
cd /c/src/picoclaw
docker build -f docker/Dockerfile -t magicform-picoclaw:latest .
```

### 2. Configure

```bash
mkdir -p deploy/data /data/workspaces/tenant-acme/{workspace,config}
cp deploy/config.example.json deploy/data/config.json
$EDITOR deploy/data/config.json   # workspace_root MUST be /data/workspaces (container path)

cp deploy/tenant.example.config.json /data/workspaces/tenant-acme/config/config.json
$EDITOR /data/workspaces/tenant-acme/config/config.json
```

### 3. Set secrets in a `.env` file (gitignored)

```bash
cat > deploy/.env <<'EOF'
PICOCLAW_CHANNELS_MAGICFORM_TOKEN=dev-shared-secret
ANTHROPIC_API_KEY=sk-ant-...
EOF
chmod 600 deploy/.env
```

### 4. Bring it up

```bash
docker compose -f deploy/docker-compose.magicform.yml up -d
docker compose -f deploy/docker-compose.magicform.yml logs -f
```

You should see `Channel enabled successfully channel=magicform` and `Listening host=0.0.0.0 port=18790`.

### 5. Smoke test against the container

```bash
DEPLOY_MODE=docker ./deploy/smoke-test.sh
```

---

## Tenant directory layout (both paths)

```
/data/workspaces/
├── default/                        # gateway's no-override workspace
│   └── sessions/                   # auto-created
└── tenant-acme/
    ├── workspace/                  # tenant runtime (sessions, scratch)
    │   ├── sessions/               # auto-created on first turn
    │   ├── AGENT.md                # copied from config/ on first turn
    │   └── skills/                 # copied from config/ on first turn
    └── config/                     # operator-managed; never written by gateway
        ├── config.json             # tenant overlay
        ├── AGENT.md                # bootstrap persona
        ├── skills/                 # bootstrap skills tree
        └── scripts/                # bootstrap scripts tree
```

`workspace/` and `config/` are both relative to `workspace_root` from the gateway's perspective. Webhook payloads send relative paths (`tenant-acme/workspace`), the agent loop validates they resolve inside the boundary, then operates on the absolute path.

---

## Verifying isolation worked

After two tenants have sent traffic:

```bash
# Each tenant's sessions live separately
ls /data/workspaces/tenant-acme/workspace/sessions/
ls /data/workspaces/tenant-globex/workspace/sessions/

# Distinct hashed agent IDs in the gateway log
docker compose -f deploy/docker-compose.magicform.yml logs picoclaw \
  | grep "Provisioned tenant agent"
# → tenant-c99f078b workspace=/data/workspaces/tenant-acme/workspace
# → tenant-1469d354 workspace=/data/workspaces/tenant-globex/workspace
```

If `workspace_root` is unset or a tenant tries to reach outside it, the gateway logs `tenant override rejected: ...` and returns 400 to the webhook caller — fail-closed by design.

---

## Operational notes

- **Tenants accumulate in memory** until gateway restart (no LRU eviction yet — see [MAGICFORM_CUSTOMIZATIONS.md](../MAGICFORM_CUSTOMIZATIONS.md#2-multi-tenancy-in-the-agent-loop) for the deferred follow-ups). Fine up to a few hundred tenants on a normal box.
- **Editing a tenant's `config.json` requires a gateway restart.** The first-build is cached for the process lifetime.
- **Operator edits to `workspace/AGENT.md` are preserved** across restarts — the bootstrap copy from `config/` is idempotent.
- **Reverse proxy + TLS** is required for production. The gateway speaks plain HTTP; terminate TLS at Caddy/Traefik/nginx in front.
- **Health endpoint:** `GET /health/magicform` returns `{"status":"ok","channel":"magicform"}` — wire it into your orchestrator.
