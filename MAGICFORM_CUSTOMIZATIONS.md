# MagicForm Customizations

This file is the index of customizations the `magicform` fork carries on top
of `sipeed/picoclaw` upstream. Read this first when:
- Doing an upstream sync (start by replaying each customization against the
  new layout if upstream has changed the surrounding code)
- Reviewing a PR that touches any of the listed files
- Onboarding a new engineer to the fork

The single source of truth for *what changed* is the git log
(`git log upstream/main..main`). This file tells you *why* and *where*.

---

## Goals of the fork

1. **Multi-tenant isolation.** A single `picoclaw` process serves many
   tenants. Each tenant has its own filesystem workspace, config, sessions,
   provider credentials, and tool/skill allowlist. Inbound messages carry
   tenant hints in `bus.InboundContext.Raw`; the agent loop validates them
   against a security boundary and applies them per-turn.
2. **Defense-in-depth on workspace boundary.** Every path-manipulating tool
   (`fs`, `exec`, skill installer) honours `agents.defaults.workspace_root`
   as a containment root. Tenants cannot read or write outside it.
3. **MagicForm webhook channel.** A webhook-driven channel
   (`pkg/channels/magicform`) accepts inbound messages from MagicForm and
   posts agent responses back via callback URL. Lives alongside upstream's
   stock channels.
4. **Bounded resource use.** Search APIs and write tools have explicit byte
   limits to keep a hostile or buggy upstream from exhausting memory/disk.

---

## Subsystem map (read top-down when syncing)

Each entry: subsystem → files touched → most recent commit on `main`.
When upstream restructures a subsystem, only the matching entry needs to be
forward-ported.

### 1. MagicForm webhook channel
- **Owns:** `pkg/channels/magicform/{magicform.go,init.go}`
- **Registers in:** `pkg/gateway/gateway.go` (blank import)
- **Config plumbing:** `MagicFormSettings` in `pkg/config/config.go`,
  `ChannelMagicForm` constant + `channelSettingsFactory` entry in
  `pkg/config/config_channel.go`, validation case in
  `pkg/channels/manager.go::getChannelConfigAndEnabled`.
- **Protocol:** HTTP POST to `/hooks/magicform` with bearer token; outbound
  is HTTP POST callback to a per-request URL with JSON payload.
- **Tenancy hints:** the webhook handler stuffs `workspace_override`,
  `config_dir`, `allowed_tools`, `allowed_skills` (and `callback_url`,
  `stack_id`, `conversation_id`) into `bus.InboundContext.Raw`; the agent
  loop reads them in `agent_tenant.go`.

### 2. Multi-tenancy in the agent loop
- **Owns (fork-only files):**
  - `pkg/agent/agent_tenant.go` — Phase 1: hint extraction and
    workspace_root validation from `bus.InboundContext.Raw`.
  - `pkg/agent/agent_tenant_registry.go` — Phase 2: per-tenant
    `AgentInstance` cache + `resolveTenantAgent` + `buildTenantAgent` +
    `applyTenantToolAllowlist` + `deriveTenantAgentID`.
  - `pkg/agent/agent_tenant_provision.go` — Phase 2: `provisionBootstrapFiles`
    that idempotently seeds new tenant workspaces from `<configDir>`.
  - Tests: `agent_tenant_test.go`, `agent_tenant_registry_test.go`.
- **Wire-up in upstream files (kept tiny):**
  - `pkg/agent/agent.go`: `processOptions` extended with four override
    fields (`WorkspaceOverride`, `ConfigDir`, `AllowedTools`,
    `AllowedSkills`) and one `tenantAgents *tenantAgentCache` field on
    `AgentLoop`.
  - `pkg/agent/agent_message.go::processMessage`: ~10-line block that
    calls `extractTenantOverrides` + `resolveTenantAgent` and substitutes
    the routed agent for the tenant clone when overrides are present.
- **Status: Phase 2 active.** Each tenant runs against an isolated
  `AgentInstance` (own workspace, sessions, ContextBuilder, Tools,
  Provider). Allowlists enforced at agent-construction time and as
  defense-in-depth at the per-turn layer.
- **Security boundary:** validation uses
  `pathutil.ResolveWorkspacePath(agents.defaults.workspace_root, hint)`;
  fails closed when `workspace_root` is unset.
- **Known follow-ups (deliberate):** no LRU eviction on the tenant
  cache (revisit when memory shows pressure); no hot-reload when a
  tenant's `configDir` changes mid-run (gateway restart picks up the
  new config); MCP tools are not in the explicit `applyTenantToolAllowlist`
  list — defense-in-depth `processOptions.AllowedTools` still covers
  them per-turn.

### 3. Workspace path security utility
- **Owns:** `pkg/pathutil/{resolve.go,resolve_test.go}` (fork-owned).
- Used by: `agent_tenant.go`, `pkg/config/config.go::mergeAgentDefaults`,
  `cmd/picoclaw/internal/agent/helpers.go::validateWorkspacePaths`,
  channels that accept tenant paths.

### 4. CLI overrides and workspace config overlay
- **Owns:** `cmd/picoclaw/internal/agent/{helpers.go,helpers_test.go}` —
  validates `--workspace` / `--config-dir` flags, loads
  `<config-dir>/config.json` and merges over the base config via
  `Config.MergeWorkspaceConfig`.
- **Owns:** `pkg/config/config.go::MergeWorkspaceConfig` and
  `mergeAgentDefaults` (fork additions; not in upstream).

### 5. Tool hardening (filesystem)
- **Owns:** customizations in `pkg/tools/fs/filesystem.go::sandboxFs.WriteFile`:
  - `MaxWriteFileSize` cap (20 MB) before opening any file.
  - `crypto/rand` temp suffixes instead of `time.Now().UnixNano()`.
- Last forward-ported: commit `cd1720f4` (after upstream `4c133dc2`
  reorganized `pkg/tools/`).

### 6. Tool hardening (web search)
- **Owns:** customizations in `pkg/tools/integration/web.go`:
  - `searchMaxResponseSize` constant (2 MB) used by every search provider's
    `io.ReadAll(io.LimitReader(...))`.
- Last forward-ported: commit `94d28c1b`.

### 7. Output-channel plumbing for tenancy callbacks
- **Owns:** `pkg/bus/types.go` additions on `OutboundMessage`: `Type`,
  `Metrics`, `Progress`, `Escalation`. Plus types `ResponseMetrics`,
  `OutboundProgress`, `OutboundEscalation`, `TokenUsage`.
- The MagicForm channel `Send` reads these to compose its callback payload.
  Other channels ignore them.

### 8. Exec tool: filterEnv
- **Owns:** `filterEnv` field in `ExecTool` and `ExecConfig`. Strips
  non-`PICOCLAW_*`-prefixed env vars before child processes.
- Files: `pkg/tools/shell.go`, `pkg/config/config.go::ExecConfig`.

### 9. Channel base hook
- **Owns:** `BaseChannel.Bus()` accessor in `pkg/channels/base.go`. Used
  by the magicform channel to publish directly with a non-default SessionKey.

### 10. Web admin hardening (web/backend)
- **Owns:** `web/backend/middleware/security_headers.go` (+ test) — sets
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and a
  baseline `Content-Security-Policy`. Wired as the outermost wrapper in
  `web/backend/main.go`'s middleware stack.
- **Owns:** `web/backend/api/errors.go` — `writeSafeError` /
  `safeErrorf` helpers that log internal errors server-side via
  `logger.ErrorCF` and return generic messages to the client.
- **Applied in:** `web/backend/api/config.go`, `launcher_config.go`,
  `gateway.go`, `pico.go`. OAuth callback in `oauth.go` also stops
  echoing `err.Error()` to the user-visible page.
- **Known follow-up:** ~50 additional `http.Error(w, fmt.Sprintf("...: %v", err), …)`
  sites in lower-impact files (`skills.go`, `models.go`, `channels.go`,
  `log.go`, etc.) still leak. Replacement is mechanical; tracked as
  follow-up work after this sync PR.

---

## Sync playbook

When pulling a new upstream:

1. `git fetch upstream && git fetch origin`
2. Branch: `git checkout -b sync/upstream-YYYY-MM-DD origin/main`
3. `git merge upstream/main` and resolve conflicts.
4. For each subsystem above, replay any commits whose files now no longer
   exist (modify/delete conflicts) onto upstream's new locations.
5. `go build ./... && go test ./...`
6. Open a PR against `main`. Each forward-port should be its own commit
   prefixed `forward-port:` so the merge commit and customization commits
   are visually distinct in `git log`.
7. Update this index if the file map shifts.

---

## What is *not* customized any more

- **Launcher (`cmd/picoclaw-launcher/`)**: dropped during the
  2026-05-07 sync. Half its dependencies were deleted upstream and
  MagicForm doesn't use the launcher's HTTP API. The launcher's
  security hardening (XSS escaping, error logging, SecurityHeaders)
  was forward-ported to upstream's replacement at `web/backend/` —
  see entry 10 above.
- **Deprecated `AgentDefaults.Model`**: dropped during the 2026-05-07
  sync. Use `model_name`. Workspace overlays that still set the old
  `"model"` JSON key will be silently ignored — migrate them.
