# Nuestra Customizations

This file is the index of customizations the **Nuestra** fork
(`Nuestra-AI/picoclaw`) carries on top of `sipeed/picoclaw` upstream. Read
this first when:
- Doing an upstream sync (start by replaying each customization against the
  new layout if upstream has changed the surrounding code)
- Reviewing a PR that touches any of the listed files
- Onboarding a new engineer to the fork

The single source of truth for *what changed* is the git log
(`git log public-main..main`). This file tells you *why* and *where*.

> **Naming.** "Nuestra" is the fork. "MagicForm" is **one product that talks
> to it**, via the `magicform` channel — the first, but not the only one.
> Fork-wide capabilities (multi-tenancy, workspace boundary, resource caps)
> are Nuestra's and are channel-agnostic; anything named `magicform` should
> refer specifically to that channel and its wire protocol.
>
> The channel's identifiers are a **live contract with deployed tenants** and
> are deliberately left alone: the `"magicform"` config key, the
> `/hooks/magicform` webhook path, the `magicform` platform/channel strings,
> `ChannelMagicForm`, and `MagicFormSettings`. Renaming any of those would
> break existing configs and callers.
>
> Env vars are the exception: they are resolved from the **channel key**, not
> from a brand baked into the code. A channel keyed `magicform` still reads
> `PICOCLAW_CHANNELS_MAGICFORM_*`, and the brand-neutral
> `PICOCLAW_CHANNELS_NUESTRA_*` applies to whichever Nuestra channel is
> configured — so a one-brand deployment need not name its brand at all. See
> `applyNuestraEnv` in `pkg/config/config_channel.go`.

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
3. **Webhook channels for Nuestra products.** A webhook-driven channel
   pattern that accepts inbound messages and posts agent responses back via
   a callback URL, alongside upstream's stock channels. `magicform`
   (`pkg/channels/magicform`) is the first such channel; additional products
   are expected to add their own rather than extend this one.
4. **Bounded resource use.** Search APIs and write tools have explicit byte
   limits to keep a hostile or buggy upstream from exhausting memory/disk.

---

## Subsystem map (read top-down when syncing)

Each entry: subsystem → files touched → most recent commit on `main`.
When upstream restructures a subsystem, only the matching entry needs to be
forward-ported.

### 1. MagicForm webhook channel (one channel, not the fork)
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
  loop reads them in `agent_tenant.go`. Those `Raw` keys are a
  **channel-agnostic contract** (entry 2) — this channel is one producer of
  them, and any future Nuestra channel can populate the same keys to get
  multi-tenancy for free.

### 2. Multi-tenancy in the agent loop (fork-wide, channel-agnostic)
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
- **Skills filtering now rides on upstream.** As of the 2026-08-05 sync,
  the fork-local `buildFilteredSkillsSummary` is gone. Upstream grew an
  equivalent `ContextBuilder.buildSkillsSummary(allowed []string)` plus
  `IncludeSkillCatalog`/`SuppressSkillContext` gating, so we adopted it.
  `SetSkillsFilter` remains (the tenant registry calls it) and now feeds
  the builder-level default for `systemPromptBuildOptions.AllowedSkills`;
  a per-request `AllowedSkills` still wins.
  - **`"*"` wildcard is preserved.** Upstream's `cleanAllowedSet`
    (`pkg/agent/turn_profile_policy.go`) lowercases and trims but has **no**
    wildcard branch, so a bare `["*"]` would match only a skill literally
    named `*` and yield an empty catalog — a silent, invisible failure.
    We keep the fork's wildcard via `containsSkillWildcard` in
    `buildSystemPromptParts`: `["*"]` collapses to a nil allowlist, which
    is upstream's own "include everything" path. The shim lives at our
    call site rather than inside `cleanAllowedSet` (upstream-owned, shared
    by other callers) to keep the next sync's merge surface small.
    Pinned by `TestSkillsFilterWildcardIncludesAllSkills` in
    `pkg/agent/context_skills_filter_test.go` — verified to fail if the
    shim is removed.
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
- **Boundary fields an overlay cannot set** (`mergeAgentDefaults`, covered by
  `pkg/config/nuestra_overlay_test.go`):
  - `workspace_root` — not copied; the root that separates tenants.
  - `allow_read_outside_workspace` — not copied. It is the only input that
    clears `readRestrict` (`pkg/agent/instance.go`), and tenant workspaces are
    siblings under `workspace_root`, so an overlay that set it could read
    across tenants. A `config_dir` is itself selected per request, so this was
    reachable from the same payload that routes a tenant.
  - `restrict_to_workspace` — merges only when `true`; an overlay may tighten
    but never clear it.
- **Sync note:** if upstream adds a negative (permission-granting) boolean to
  `AgentDefaults`, it must be excluded here too. The "merge only when true"
  idiom is fail-safe for positive flags and fail-open for negative ones.

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
- Last forward-ported: 2026-08-05 sync (upstream `49183d7e`). Upstream has
  since added `_ = resp.Body.Close()` at each site and its own `1<<20` cap
  on the Sogou provider; the merge keeps our cap everywhere else and the
  tighter upstream bound at Sogou.

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

### 11. CodeQL workflow
- **Owns:** `.github/workflows/codeql.yml` — fork-only; upstream has no
  CodeQL workflow, so this file should never conflict on sync. Runs on
  pushes and PRs to `main` plus a weekly schedule, over `go` and
  `javascript-typescript`.
- **Why:** complements the `govulncheck` job in `pr.yml`. `govulncheck`
  reports known CVEs in dependencies; CodeQL analyzes this repo's own code
  for injection, path traversal, and similar defects — the same class of
  logic as the workspace-boundary enforcement in entries 3 and 5.
- **Gotcha:** the Go autobuild needs `GOFLAGS=-tags=goolm,stdjson`. Without
  the tags, a bare `go build ./...` fails on the Matrix channel's libolm
  binding under `CGO_ENABLED=0` — the same reason the lint job passes
  `--build-tags=goolm,stdjson`. Set it in a step **after** `init`: `init`
  warns that forwarding `GOFLAGS` through it is deprecated, and autobuild
  runs during `analyze`, so a later step still covers the build.
- **Repo settings (not in git):** secret scanning, push protection, and
  Dependabot alerts are enabled via repo settings rather than a file, so
  they do not travel with a clone. Dependabot *security updates* is still
  off — enabling it means automated PRs, which is a workflow choice.

---

## Clone setup: never let anything default to upstream

This is a fork of `sipeed/picoclaw`. Git and `gh` both default to the
**parent** repo in a fork, so a fresh clone will happily try to push
branches and open PRs against upstream. Run this once per clone:

```bash
# 1. gh: PRs/issues resolve to our fork, not the parent.
#    Without this, `gh pr create` targets sipeed/picoclaw.
gh repo set-default Nuestra-AI/picoclaw

# 2. Make the upstream remote fetch-only. Pushing is then impossible,
#    not merely discouraged.
git remote set-url --push upstream DISABLED_use_origin

# 3. Pin push/checkout defaults to origin.
git config remote.pushDefault origin
git config checkout.defaultRemote origin
git config push.default simple
```

Why all four, rather than just one:
- `remote.pushDefault=origin` + `push.default=simple` means a branch that
  tracks `upstream` **refuses to push at all** ("cannot resolve 'simple'
  push to a single destination") instead of silently pushing to sipeed.
  This is the safety net for trap 1 below, where `git branch -f` keeps
  resetting `public-main`'s tracking back to `upstream`.
- The disabled push URL is belt-and-braces: it makes an explicit
  `git push upstream ...` fail fast with an obvious message.
- `gh repo set-default` is separate from git config — git settings do
  **not** affect where `gh pr create` opens a PR.

Verify with `git rev-parse --abbrev-ref '<branch>@{push}'` — it must print
an `origin/...` ref for any branch you intend to push.

Also worth clearing on old branches: `git config --get-regexp
'github-pr-base-branch'`. Stale `sipeed#picoclaw#main` entries left by
editor tooling will pre-fill the wrong PR base.

---

## Sync playbook

When pulling a new upstream:

0. Know your two reference points:
   - `public-main` — a **pristine mirror of `upstream/main`**, pushed to
     `origin` so it is visible to the whole team and to CI without anyone
     configuring the upstream remote. It is fast-forward-only and must
     **never** receive a commit.
   - `main` — the fork. `git log public-main..main` is the true fork delta,
     and `git diff public-main main -- <path>` shows what we changed in any
     file without needing the upstream remote configured.

   **Refreshing `public-main` is a deliberate manual step, not automation.**
   It is force-updated, so it is left in human hands rather than on a timer.
   Do it at the start of each sync — never on a schedule:

   ```bash
   git fetch upstream
   git branch -f public-main upstream/main            # fast-forward the mirror
   git push --force origin public-main:public-main    # explicit refspec, never a bare push
   git branch --set-upstream-to=origin/public-main public-main   # see trap 1
   ```

   Three traps, all real (each was hit while setting this up):
   1. **`git branch -f` resets tracking to `upstream` every single time.**
      A later bare `git push` on this branch would then target **sipeed**.
      That is why the push above uses an explicit `origin <src>:<dst>`
      refspec and the tracking is re-pinned afterwards. Verify with
      `git config branch.public-main.remote` — it must print `origin`.
   2. **Never `git checkout public-main` and commit.** If it diverges it
      stops being a mirror and the next sync's merge-base is silently
      wrong. GitHub branch protection (`lock_branch`) now blocks this;
      recover by re-running the refresh above.
   3. **The mirror is force-updated, so it is deliberately manual.** Do
      not put it on a schedule — an unattended job holding force-push
      rights is a poor trade for a command run a few times a year.
      Branch protection keeps `allow_force_pushes` on with
      `enforce_admins` off precisely so this manual refresh still works.
1. `git fetch upstream && git fetch origin`
2. Branch: `git checkout -b sync/upstream-YYYY-MM-DD origin/main`
3. `git merge upstream/main` and resolve conflicts.
   Preview the blast radius first — this lists conflicts without touching
   the worktree: `git merge-tree --write-tree --name-only main upstream/main`
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
