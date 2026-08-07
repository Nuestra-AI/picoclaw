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

Bootstrap items are `AGENT.md` or `AGENTS.md`, `IDENTITY.md`, `SOUL.md`,
`USER.md`, `skills/`, and `scripts/`. Missing items are skipped. Files already
in the workspace are kept, not overwritten, so editing a tenant's `workspace/`
copy survives later provisioning — update the workspace copy directly, or run
`picoclaw agent --config-dir ... --refresh` to force the config copy back over
it.

#### What a tenant overlay can and cannot change

The overlay is merged field-by-field over the gateway's base config, and only
non-empty fields apply. Boundary fields are the exceptions:

- **`workspace_root` is ignored.** It is the security boundary that keeps
  tenants apart, so it can only be set in the gateway's base config. Putting it
  in a tenant overlay silently does nothing.
- **`allow_read_outside_workspace` is ignored.** It is the only setting that
  can clear the read boundary, and tenant workspaces are siblings under
  `workspace_root` — an overlay that could set it would let one tenant read
  another's files. Base config only. (Fork change; upstream honors it from
  overlays.)
- **`workspace` is validated, not trusted.** An overlay may point a tenant at a
  different workspace, but the path is resolved against `workspace_root` and
  rejected if it escapes.
- **`restrict_to_workspace` merges only when `true`.** An overlay can tighten
  it but never clear it.

Everything else — `model_name`, `max_tokens`, `temperature`, `provider`,
`max_tool_iterations`, and the rest — overrides normally when set.

### 5. Set secrets via env

The channel's shared secret is a `SecureString`; set it via env, not config.json:

```bash
export PICOCLAW_CHANNELS_NUESTRA_TOKEN="dev-shared-secret"
export ANTHROPIC_API_KEY="sk-ant-..."
```

`PICOCLAW_CHANNELS_NUESTRA_*` is the brand-neutral form and applies to
whichever Nuestra channel is configured, so it needs no change when the brand
does. To run several brands in one process, give each one a key-scoped
variable named after its key in `channel_list` — `PICOCLAW_CHANNELS_MAGICFORM_TOKEN`,
`PICOCLAW_CHANNELS_OTHERBRAND_TOKEN` — which takes precedence over the neutral
form. Each brand needs its own token and `webhook_path`.

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
PICOCLAW_CHANNELS_NUESTRA_TOKEN=dev-shared-secret
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

### Skill format

`skills/` is not a free-form tree. It holds **one directory per skill**, each
containing a `SKILL.md`:

```
config/skills/
├── pdf-tools/
│   ├── SKILL.md          # required — without it the directory is not a skill
│   └── scripts/          # optional; runnable when the exec tool is enabled
└── invoice-parser/
    └── SKILL.md
```

Discovery is **one level deep**. `skills/category/pdf-tools/SKILL.md` is never
found. A directory without `SKILL.md` is silently ignored rather than
reported, so a typo in the filename looks like the skill simply not existing.

`SKILL.md` needs YAML frontmatter. Only two keys are read — everything else
(`homepage`, `metadata`, …) is preserved but ignored by the loader:

```markdown
---
name: pdf-tools
description: Extract text and tables from PDFs. Use when the user shares a PDF.
---

# PDF Tools

## When to use
...
```

`description` is what the agent sees when deciding whether to load the skill,
so write it as a trigger condition rather than a summary.

Names must be alphanumeric with single hyphens (`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`),
64 characters at most. A name that fails the pattern is skipped silently.

### Which skill wins

Three roots are searched, and the order is not what you would guess:

1. **Built-ins always win.** A workspace skill cannot shadow a built-in name.
   This is a veto, not a preference: name a skill `github` or `summarize` and
   your version is ignored with no error.
2. **Workspace `skills/`** — the tenant's own tree, seeded from `config/`.
3. **Global `~/.picoclaw/skills`** — last resort.

Check the built-in names before choosing yours; a collision fails silently.

### Two ways skills arrive

These are independent mechanisms, and only the first is active by default:

- **Provisioning (no tool flags involved).** Skills in `config/skills/` are
  copied into the tenant workspace on first turn and loaded through a path
  with **no tool gate on it**. This works regardless of the `find_skills` and
  `install_skill` settings below.
- **Registry install.** The agent fetches skills at runtime from clawhub or
  GitHub. Off by default; see [Curating the skill registries](#curating-the-skill-registries).

If you author the skills, provisioning is the simpler path and needs none of
the registry configuration.

---

## What defines a tenant

**`workspace` is the tenant boundary.** Nothing else is.

The gateway keys its per-tenant agents on `workspace` + `configDir`
(`tenantKey` in `pkg/agent/agent_tenant_registry.go`). Two requests carrying
the same `workspace` are the same tenant — same agent instance, same
filesystem, same session store — regardless of what else the payload says.

`stackId` does **not** participate. It scopes the session key
(`agent:main:magicform:{stackId}:{conversationId}`), which separates
conversation history *within* whatever workspace was given. Sending different
`stackId` values with one `workspace` yields separate conversations sharing one
filesystem — not separate tenants.

There is no `accountId` field in the gateway. If your backend has accounts,
stacks, or projects, you decide which of them maps to `workspace`.

### Choosing what `workspace` means

The gateway does not care what the string represents, only that it is stable
and unique per isolation unit. Two common choices:

| Scheme | `workspace` | Isolation unit |
|---|---|---|
| Per account | `acct-1234/workspace` | One filesystem per customer; their stacks share files and see each other's data. |
| Per stack | `acct-1234/stack-7/workspace` | One filesystem per stack; the same customer's stacks cannot read each other. |

Pick per-account when a customer's stacks should share uploaded files, notes,
or scratch state. Pick per-stack when they must not — for example when
different stacks belong to different end users.

The rules that matter either way:

- **Stable across requests.** A given account (or stack) must always receive
  the identical `workspace` string. A new value means a new empty workspace,
  re-provisioned from `configDir` with no prior session history.
- **Unique per isolation unit.** Two units must never share a `workspace`;
  that is precisely what filesystem isolation is.
- **Exact string match.** Keys are compared as strings, so
  `acct-1234/workspace` and `acct-1234/workspace/` are two different tenants
  pointing at one directory. Normalize before sending.
- **Cardinality has a cost.** Tenant agents are cached and **never evicted**
  until the gateway restarts, so each distinct `workspace` holds an agent and
  its provider for the process lifetime. Per-stack multiplies that count by
  stacks-per-account; size accordingly.

`configDir` is part of the key too, so the same `workspace` with two different
`configDir` values produces two agents over one directory. Keep `configDir` a
deterministic function of the same unit — usually a sibling of `workspace`.

The hashed agent ID in the logs (`tenant-c99f078b`) is
`sha256(workspace)[:4]`, so it changes whenever the workspace string changes.
That is the quickest way to confirm two requests landed on the same tenant.

---

## Per-tenant routing and privileges

Tenant scoping is **per request**, not per config file. The four fields below
travel in the webhook payload, so the calling backend decides what each tenant
gets — there is no static list of tenants in `config.json`.

| Payload field | Effect |
|---|---|
| `workspace` | Working directory, relative to `workspace_root`. Required for any tenant routing. |
| `configDir` | Config overlay + bootstrap source, relative to `workspace_root`. |
| `allowedTools` | Tool allowlist for the turn. **Omit or leave empty to allow every enabled tool.** |
| `allowedSkills` | Skill filter for the turn. Omit or leave empty to load every skill. |

```jsonc
{
  "stackId": "s1",
  "conversationId": "c1",
  "userId": "u1",
  "message": "hello",
  "workspace": "tenant-acme/workspace",
  "configDir": "tenant-acme/config",
  "allowedTools": ["read_file", "web_fetch"],
  "allowedSkills": ["summarize"]
}
```

`allowedTools` is how one tenant gets shell access and another does not. Note
the default: an **absent or empty** list means no filtering, so a caller that
forgets the field grants everything the gateway has enabled. Send an explicit
allowlist for any tenant that should be restricted, and keep `tools.exec`
disabled in the base config if no tenant should ever reach a shell.

These filter what a turn may use; they do not widen it. A tool disabled in the
gateway config cannot be re-enabled by listing it here.

The CLI takes the same four as flags — `--workspace`, `--config-dir`,
`--tools`, `--skills` — which is what the smoke test uses to exercise the path
without a backend.

---

## Curating the skill registries

A skill is markdown injected into the agent's prompt plus a `scripts/` tree
the exec tool can run. Installing one is closer to running third-party code
than to fetching data.

Upstream ships two registries — `clawhub` (`https://clawhub.ai`) and `github`
(`https://github.com`) — and both default to **enabled**, as do the
`find_skills` and `install_skill` tools. Upstream applies no allowlist: the
`github` registry resolves any `owner/repo` and fetches it. With
`install_skill` reachable from a tenant turn, the install target is
attacker-controlled.

`config.example.json` therefore ships with all of it off:

```json
"find_skills":   { "enabled": false },
"install_skill": { "enabled": false }
```

**This costs you nothing if you provision skills yourself.** Skills placed in
a tenant workspace — including the `skills/` tree the bootstrap copies from
`config_dir` — load through a path with no tool gate on it. The operator
seeds skills; tenants use them. The two tools above only govern fetching
*new* skills from a remote registry.

### What the two tools do

They are independent, and one combination is a trap:

| `find_skills` | `install_skill` | Effect |
|---|---|---|
| off | off | Default. No registry is constructed; the allowlist below is dormant. |
| on | off | Agent can browse but not install. Search results are filtered to the allowlist. |
| off | on | **Avoid.** Discovery is hidden but installing is not blocked. |
| on | on | Full self-service, bounded by the allowlist. |

The third row is the trap: **`install_skill` never calls search.** It takes a
slug straight from its tool arguments, so the agent can install anything it
can name — from training data or from the conversation — without
`find_skills`. Turning discovery off hides the catalog without closing the
install path. Before the allowlist existed, that meant any public GitHub repo.

With both on, the allowlist is what bounds installs; with `install_skill`
off, it is only defense in depth.

### Turning discovery on with an allowlist

If tenants need to install skills themselves, enable the tools and list what
they may install. Each registry takes a `param.allow` list:

```json
"find_skills":   { "enabled": true },
"install_skill": { "enabled": true },
"skills": {
  "enabled": true,
  "registries": [
    { "name": "clawhub", "enabled": false, "base_url": "https://clawhub.ai" },
    {
      "name": "github",
      "enabled": true,
      "base_url": "https://github.com",
      "param": {
        "allow": [
          "Nuestra-AI/skills@v1.4.0",
          "some-vendor/pdf-tools"
        ]
      }
    }
  ]
}
```

Rules, in the order they bite:

- **An empty `allow` denies everything.** A dropped key fails closed rather
  than opening the registry to the whole internet.
- **`owner/repo@ref` pins the version**, and the pin wins over whatever
  version the agent asks for. Without a pin the agent chooses the ref, so an
  allowed repo can still change under you. Prefer pinning to a tag or commit.
- **A subpath under an allowed repo is permitted** — the repo is the vetted
  unit, so `Nuestra-AI/skills/pdf-tools` passes if `Nuestra-AI/skills` is
  listed. (GitHub only; clawhub identifiers cannot contain `/`.)
- **Matching is on whole path segments**, so `nuestra-ai-evil/skills` does
  not match `Nuestra-AI/skills`. For github entries, case, a `.git` suffix, a
  full `https://github.com/...` URL (including a GitHub Enterprise base path),
  and a trailing slash all normalize to the same entry.
- **Clawhub slugs are matched whole.** They are opaque single segments, so
  `summarize` does not admit `summarize@anything` — unlike github targets,
  no suffix is stripped before comparison.
- **A github entry must name owner *and* repo.** `Nuestra-AI` alone is
  rejected rather than treated as every repo that owner has, since the
  subpath rule would otherwise turn a typo into an org-wide wildcard. Only
  `http`/`https` URLs are recognized, and a URL's host must match the
  registry's configured `base_url`.
- **Case affects matching only, never the fetch.** GitHub serves a repo under
  any casing, so the allowlist compares case-insensitively — otherwise an
  entry written `nuestra-ai/skills` would be bypassed by a request for
  `Nuestra-AI/skills`, which is the same repo. The install itself uses the
  casing as requested, so the fetched URL and the created
  `workspace/skills/<name>` directory keep it. Write entries in the repo's
  canonical case (`Nuestra-AI/skills`) so the config reads as the truth.
- **Search results are filtered to the allowlist**, so the agent is not shown
  skills that `install_skill` would then refuse.
- Clawhub entries are bare slugs (`pdf-tools`), not `owner/repo`.

**The allowlist bounds the agent, not you.** `picoclaw skills install` and the
launcher's admin API are operator-driven and stay unrestricted — an allowlist
that blocked them would break the workflow that seeds skills in the first
place. Only skills the agent installs during a tenant turn are gated.

### `version` must be set in your config

`config.example.json` carries `"version": 3`. Keep it.

Without it, `LoadConfig` treats the file as legacy and runs the migration
chain on startup, which rebuilds the registries and **discards `param`
without an error**. The allowlist would load, report itself active, and block
everything — including the repos you listed.

The same migration rewrites `config.json` in place (expanding every default,
leaving a `config.json.<date>.bak`) and needs the config directory to be
writable — `MakeBackup` failing is fatal, so a read-only config mount without
`version` set means the gateway will not start. With `version` present none
of this runs.

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

- **Tenants accumulate in memory** until gateway restart (no LRU eviction yet — see [NUESTRA_CUSTOMIZATIONS.md](../NUESTRA_CUSTOMIZATIONS.md#2-multi-tenancy-in-the-agent-loop) for the deferred follow-ups). Fine up to a few hundred tenants on a normal box.
- **Editing a tenant's `config.json` requires a gateway restart.** The first-build is cached for the process lifetime.
- **Operator edits to `workspace/AGENT.md` are preserved** across restarts — the bootstrap copy from `config/` is idempotent.
- **Reverse proxy + TLS** is required for production. The gateway speaks plain HTTP; terminate TLS at Caddy/Traefik/nginx in front.
- **Health endpoint:** `GET /health/<channel-key>` returns `{"status":"ok","channel":"<channel-key>"}` — `/health/magicform` for the `magicform` channel in `config.example.json`. The path derives from the channel's key in `channel_list`, so renaming the key moves the endpoint; keep the compose healthcheck in step. Wire it into your orchestrator.
