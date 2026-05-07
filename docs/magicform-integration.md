# MagicForm Integration Spec

MagicForm delegates agentic tasks to PicoClaw via webhooks. PicoClaw processes the request asynchronously and POSTs the result back to a callback URL.

> For general PicoClaw installation, CLI usage, and config reference, see [cli.md](cli.md).

---

## Pre-requisites

1. **Install PicoClaw** — see [cli.md § Install](cli.md#install)
2. **Global config** — see [cli.md § Global Config](cli.md#global-config)

### Directory layout

MagicForm pre-provisions directories on disk before calling PicoClaw (see [cli.md § Directory Layout](cli.md#directory-layout) for the general structure):

```
{workspace_root}/
  {stackId}/
    config/                    # configDir -- shared per-stack
      config.json              # API key, model, agent settings for this stack
      AGENTS.md                # Agent instructions (optional)
      IDENTITY.md              # Agent identity (optional)
      SOUL.md                  # Agent personality (optional)
      USER.md                  # User context (optional)
    {conversationId}/          # workspace -- per-conversation
      sessions/                # Conversation history (managed by PicoClaw)
      memory/                  # Persistent agent memory (managed by PicoClaw)
      skills/                  # Workspace-local skills (optional)
```

### Workspace config

Per-stack config overlays are placed in the config directory. See [cli.md § Workspace Config](cli.md#workspace-config) for merge rules and example.

---

## Gateway Mode

### Channel config

Add to `~/.picoclaw/config.json`. Channels now use the upstream map+settings shape: each channel is keyed under `channel_list` and its provider-specific options live under `settings`.

```jsonc
{
  "agents": {
    "defaults": {
      "workspace_root": "/data/workspaces"
    }
  },
  "gateway": {
    "host": "0.0.0.0",
    "port": 18790
  },
  "channel_list": {
    "magicform": {
      "enabled": true,
      "type": "magicform",
      "allow_from": [],
      "settings": {
        "backend_url": "https://api.magicform.example.com",
        "webhook_path": "/hooks/magicform"
      }
    }
  }
}
```

The `token` field is a `SecureString`: it is **not** stored in `config.json` in plaintext. Set it once via environment variable or `picoclaw auth set` and it lands in `~/.picoclaw/.security.yml` (the security side-store), not the JSON config.

| Field | Location | Description | Default |
|-------|----------|-------------|---------|
| `enabled` | top-level | Enable the channel. | `false` |
| `type` | top-level | Always `"magicform"` (matches the channel factory key). | — |
| `allow_from` | top-level | Sender ID allowlist. Empty = allow all. Accepts strings and numbers (e.g. `["user1", 12345]`). | `[]` |
| `settings.token` | secure store | Bearer token for webhook auth. Empty = allow all (dev only). Stored in `.security.yml`, not `config.json`. | `""` |
| `settings.backend_url` | settings | Fallback callback URL base (used when payload omits `callbackUrl`). | `""` |
| `settings.webhook_path` | settings | HTTP path for the webhook endpoint. | `/hooks/magicform` |
| `settings.workspace_root` | settings | Channel-level override for workspace path validation root. If not set, falls back to `agents.defaults.workspace_root`. At least one must be configured. | `""` |

**`workspace_root` resolution**: The MagicForm channel determines its effective workspace root using:
1. `channels.magicform.workspace_root` (channel-level override), if set.
2. `agents.defaults.workspace_root` (global), if set.
3. If neither is configured, the gateway **fails to start** with an error. A workspace root is required for MagicForm because all webhook paths must be validated against a boundary.

The recommended approach is to set `workspace_root` once in `agents.defaults` so that it applies to both the CLI and all gateway channels. Use the channel-level `workspace_root` only if MagicForm needs a different root than other entry points.

Most settings can be set via environment variables:

```bash
PICOCLAW_CHANNELS_MAGICFORM_TOKEN=your-shared-secret
PICOCLAW_CHANNELS_MAGICFORM_BACKEND_URL=https://api.magicform.example.com
PICOCLAW_CHANNELS_MAGICFORM_WEBHOOK_PATH=/hooks/magicform
PICOCLAW_CHANNELS_MAGICFORM_WORKSPACE_ROOT=/data/workspaces
PICOCLAW_CHANNELS_MAGICFORM_ALLOW_FROM=sender1,sender2
PICOCLAW_AGENTS_DEFAULTS_WORKSPACE_ROOT=/data/workspaces
```

The `enabled` flag and `type` are not env-bindable — set them in `config.json` (or YAML). Setting `PICOCLAW_CHANNELS_MAGICFORM_TOKEN` populates the secure store entry on next save.

### Start the gateway

```bash
picoclaw gateway
# or with debug logging:
picoclaw gateway -d
```

Listens on `{host}:{port}` (default `127.0.0.1:18790`).

### Health check

```
GET /health/magicform
```

Response:

```json
{"status": "ok", "channel": "magicform"}
```

### Webhook: send a message

```
POST /hooks/magicform
Authorization: Bearer your-shared-secret
Content-Type: application/json
```

#### Request body

```json
{
  "stackId": "s1",
  "conversationId": "c1",
  "userId": "user-123",
  "message": "Summarize the latest sales report",
  "workspace": "s1/c1",
  "configDir": "s1/config",
  "callbackUrl": "https://api.magicform.example.com/claw-agent/callback",
  "allowedTools": ["read_file", "web_fetch"],
  "allowedSkills": ["summarize"]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `stackId` | string | Yes | Tenant/stack identifier. |
| `conversationId` | string | Yes | Conversation identifier. |
| `userId` | string | No | Sender identifier (defaults to `"anonymous"`). |
| `message` | string | Yes | The user's message. |
| `workspace` | string | No | Agent working directory, relative to `workspace_root`. |
| `configDir` | string | No | Config directory, relative to `workspace_root`. Contains `config.json` and bootstrap files. |
| `callbackUrl` | string | No | Where to POST the response. Falls back to `backend_url + "/claw-agent/callback"`. |
| `allowedTools` | string[] | No | Tool allowlist. Empty = all tools enabled. See [cli.md § Tool Names](cli.md#tool-names-reference). |
| `allowedSkills` | string[] | No | Skill filter. Empty = all skills loaded. |

**Path security**: `workspace` and `configDir` must be relative paths that resolve to subdirectories under `workspace_root`. The following are rejected with `400 Bad Request`:
- Absolute paths (e.g. `/data/workspaces/s1/c1`)
- Directory traversal (`../escape`, `a/../../etc`, `foo/..`)
- Empty string or bare `.` (would resolve to root itself)

See [cli.md § Path Security](cli.md#path-security) for the full validation rules shared across CLI and gateway.

**Request size limit**: Webhook payloads are limited to 1 MB. Larger requests receive `413 Request Entity Too Large`.

**Method**: Only `POST` is accepted. Other methods receive `405 Method Not Allowed`.

#### Response

Returns `200 OK` immediately. Processing happens asynchronously.

### Callback: receive the result

PicoClaw POSTs results back to the callback URL. There are three callback types: **final**, **progress**, and **escalation**.

```
POST {callbackUrl}
Authorization: Bearer your-shared-secret
Content-Type: application/json
```

#### Common fields

Every callback includes these fields:

| Field | Type | Description |
|-------|------|-------------|
| `stackId` | string | Echoed from request. |
| `conversationId` | string | Echoed from request. |
| `taskId` | string | Unique task ID: `claw_task_{conversationId}_{createdAtMs}`. |
| `type` | string | `"final"`, `"progress"`, or `"escalation"`. |
| `status` | string | `"success"` or `"error"`. |
| `response` | string | The agent's response text (may be empty for progress/escalation). |
| `runtime` | string | Always `"picoclaw"`. |

#### Final callback

Sent once when the agent finishes processing. Includes execution metrics.

```json
{
  "stackId": "s1",
  "conversationId": "c1",
  "taskId": "claw_task_c1_1709712000000",
  "type": "final",
  "status": "success",
  "response": "Here is the summary of the latest sales report...",
  "runtime": "picoclaw",
  "durationMs": 4523,
  "tokenUsage": {
    "promptTokens": 1200,
    "completionTokens": 350,
    "totalTokens": 1550,
    "model": "anthropic/claude-sonnet-4.6"
  },
  "toolCalls": 3,
  "progress": null,
  "escalation": null
}
```

| Field | Type | Description |
|-------|------|-------------|
| `durationMs` | number | Total processing time in milliseconds. |
| `tokenUsage` | object \| null | Token consumption breakdown. |
| `tokenUsage.promptTokens` | number | Prompt tokens used across all iterations. |
| `tokenUsage.completionTokens` | number | Completion tokens used across all iterations. |
| `tokenUsage.totalTokens` | number | Total tokens (prompt + completion). |
| `tokenUsage.model` | string | Model ID used for generation. |
| `toolCalls` | number | Total tool calls made during processing. |
| `error` | string | Error message (present only when `status` is `"error"`). |
| `progress` | null | Always null for final callbacks. |
| `escalation` | null | Always null for final callbacks. |

#### Progress callback

Sent during processing as the agent executes tools. Multiple progress callbacks may be sent before the final callback.

```json
{
  "stackId": "s1",
  "conversationId": "c1",
  "taskId": "claw_task_c1_1709712000000",
  "type": "progress",
  "status": "success",
  "response": "Running tools: web_fetch",
  "runtime": "picoclaw",
  "progress": {
    "status": "thinking",
    "toolName": "web_fetch",
    "stepNumber": 2,
    "message": "Running tools: web_fetch"
  },
  "escalation": null
}
```

| Field | Type | Description |
|-------|------|-------------|
| `progress` | object | Progress details. |
| `progress.status` | string | Current status (e.g. `"thinking"`). |
| `progress.toolName` | string | Name of the tool being executed. |
| `progress.stepNumber` | number | Current iteration number. |
| `progress.message` | string | Human-readable progress description. |

#### Escalation callback

Sent when the agent determines it needs human intervention.

```json
{
  "stackId": "s1",
  "conversationId": "c1",
  "taskId": "claw_task_c1_1709712000000",
  "type": "escalation",
  "status": "success",
  "response": "",
  "runtime": "picoclaw",
  "progress": null,
  "escalation": {
    "reason": "User requested human support",
    "notes": "Customer issue requires billing system access"
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `escalation` | object | Escalation details. |
| `escalation.reason` | string | Why escalation is needed. |
| `escalation.notes` | string | Additional context (optional). |

### Session isolation

Each request gets a unique session key: `agent:main:magicform:{stackId}:{conversationId}`.

Sessions are stored at `{workspace}/sessions/`. Different conversations within the same stack share the config directory (API keys, bootstrap files) but have separate workspace directories, sessions, and memory.

**Multi-tenancy is active.** The first time the gateway sees a tenant (keyed by `workspace` + `configDir` from the webhook payload) it provisions a fresh `AgentInstance` rooted at the tenant's workspace, applies the workspace-local `<configDir>/config.json` overlay, resolves the tenant's LLM provider from the merged config, and registers the agent under a stable hashed ID. Subsequent turns for the same tenant route to the cached instance. Agents are not evicted; they live for the gateway process's lifetime.

What this means for you, the operator:

- **Sessions** are per-tenant by virtue of each tenant's `AgentInstance` having its own `Sessions` store rooted at `{workspace}/sessions/`. Two tenants never share history.
- **Provider credentials** can be per-tenant — set `model_list` (and optionally `agents.defaults.model_name`/`provider`) inside `<configDir>/config.json`. `MergeWorkspaceConfig` overlays it onto the gateway's base config before the tenant agent's provider is constructed.
- **Tool allowlist** (`allowedTools` in the webhook payload) disables tools at registration time on the tenant agent — the LLM never sees disallowed tools and can't invoke them. Defense-in-depth: the per-turn `AllowedTools` check on `processOptions` is the second layer.
- **Skill allowlist** (`allowedSkills`) sets the tenant agent's `ContextBuilder.SkillsFilter` so only listed skills appear in the system prompt.
- **Filesystem boundary**: every filesystem tool (`read_file`, `write_file`, `exec`, etc.) is constructed with the tenant workspace baked in — physically can't escape, even if the in-tenant code attempted to. Combined with the `workspace_root` validation at webhook ingress, this is the load-bearing isolation invariant.

### Processing flow

```
MagicForm                          PicoClaw Gateway
    |                                     |
    |-- POST /hooks/magicform ----------->|
    |                              validate token
    |                              parse payload
    |                              ResolveWorkspacePath(root, workspace)  ← 400 on failure
    |                              ResolveWorkspacePath(root, configDir)  ← 400 on failure
    |<------------ 200 OK ---------------|
    |                                     |
    |                              publish InboundMessage to bus (Context.Raw carries hints)
    |                                     |
    |                              Agent loop:
    |                                extractTenantOverrides — re-validate against workspace_root
    |                                attach overrides to processOptions
    |                                resolveTenantAgent — cache lookup or first-build:
    |                                    LoadWorkspaceConfig(configDir)
    |                                    MergeWorkspaceConfig into cloned cfg
    |                                    pin tenant workspace
    |                                    applyTenantToolAllowlist
    |                                    providerFactory(tenant model_config)
    |                                    NewAgentInstance(...)
    |                                    SetSkillsFilter(allowedSkills)
    |                                    provisionBootstrapFiles(configDir, workspace)
    |                                run LLM iterations on the tenant agent:
    |                                run LLM iterations:
    |                                  for each iteration:
    |                                    LLM call → accumulate token usage
    |                                    tool calls → accumulate tool count
    |                                     |
    |<-- POST callbackUrl (progress) ----|  ← during tool execution
    |                                     |
    |                                  ... more iterations ...
    |                                     |
    |<-- POST callbackUrl (final) -------|  ← with metrics (duration, tokens, tool calls)
```

---

## Testing with CLI

You can test the same workspace/config setup without the gateway using `picoclaw agent`. See [cli.md § picoclaw agent](cli.md#picoclaw-agent) for full flag reference.

```bash
# One-shot with tenant isolation (same relative paths MagicForm would use)
picoclaw agent -m "Summarize the report" \
  -s s1:c1 \
  --workspace s1/c1 \
  --config-dir s1/config

# Restricted tools, matching a webhook allowedTools filter
picoclaw agent -m "Search the web for recent news" \
  --tools web,web_fetch

# Debug mode to see session key, model, and iteration details
picoclaw agent -d -m "Hello" -s test
```

> **Note**: The CLI resolves `--workspace` and `--config-dir` against `agents.defaults.workspace_root` using the same validation as the MagicForm webhook. Both entry points use the shared `pathutil.ResolveWorkspacePath` function.

---

## Troubleshooting

**Webhook returns 405 Method Not Allowed**
- The endpoint only accepts `POST` requests. Ensure you are not sending a `GET` or other method.

**Webhook returns 401 Unauthorized**
- Check that the `Authorization: Bearer {token}` header matches the `token` in the MagicForm channel config. Token comparison uses constant-time comparison.

**Webhook returns 400 "Invalid workspace" or "Invalid configDir"**
- The `workspace` or `configDir` path failed validation. Common causes:
  - **Absolute path** — use `s1/c1`, not `/data/workspaces/s1/c1`.
  - **Traversal** — paths like `../escape` or `a/../../etc` are rejected.
  - **Empty or `.`** — the path must be a subdirectory, not root itself.
  - **Escapes root** — the resolved path lands outside `workspace_root`.

**Webhook returns 413 Request Entity Too Large**
- The request payload exceeds the 1 MB limit. Reduce the message size.

**Gateway fails to start: "magicform channel requires workspace_root"**
- Neither `channels.magicform.workspace_root` nor `agents.defaults.workspace_root` is configured. Set at least one. The recommended location is `agents.defaults.workspace_root`.

**Callback not received**
- Check that `callbackUrl` in the payload or `backend_url` in config is reachable from PicoClaw.
- Check PicoClaw logs for callback errors.
- Request contexts expire after 10 minutes.
- Progress and escalation callbacks keep the request context alive. The final callback deletes it.

**Unexpected `null` in progress/escalation fields**
- `progress` and `escalation` are always present in every callback but set to `null` when not applicable (e.g. `progress` is `null` in a final callback). Check the `type` field to determine which sub-object to read.

**Session not persisting across requests**
- Ensure the same `workspace` path is sent for the same conversation.
- Sessions are stored at `{workspace}/sessions/`. Different workspace paths = different sessions.
