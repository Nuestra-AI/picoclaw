// PicoClaw - Per-tenant AgentInstance provisioning for multi-tenant magicform.
//
// This file is the magicform fork's primary multi-tenancy implementation
// on top of upstream's agent registry. It provisions a fresh AgentInstance
// per tenant the first time we see one (keyed by tenant ID derived from
// stack_id + workspace), caches it, and routes inbound tenant messages to
// the right instance.
//
// Why a separate registry rather than mutating upstream's AgentRegistry:
//
//   - Sync friendliness: AgentRegistry is upstream code. Adding methods
//     onto it would create merge conflicts every sync. Keeping the cache
//     in our own struct here means future upstream changes to the registry
//     (resolver tweaks, lifecycle hooks, etc.) don't conflict with us.
//
//   - Isolation guarantees: each tenant gets a *real* AgentInstance built
//     by upstream's NewAgentInstance, with its own Workspace, Sessions,
//     Provider, Tools, ContextBuilder. Every isolation invariant upstream
//     already enforces (per-tool workspace boundary, per-agent session
//     directory, etc.) is automatically inherited — we don't reimplement
//     it.
//
//   - Dynamic tenants: MagicForm provisions stacks at runtime. Static
//     `agents.list[]` entries don't fit. We build on first sight, cache,
//     and reuse.
//
// Concurrency:
//
//   - getOrCreateTenantAgent uses a single mutex for the cache map plus
//     per-key once-per-creation semantics so two concurrent first-message
//     turns for the same tenant don't both build agents.
//
//   - Each cached AgentInstance is itself shared across the tenant's
//     turns; concurrency safety inside one tenant is the same as upstream
//     guarantees for any single agent (per-session locks in SessionStore,
//     stateless Provider, etc.).

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// tenantAgentCache maps a tenant key (derived from workspace + config_dir)
// to a provisioned AgentInstance. Lifetime: the lifetime of the AgentLoop.
// Eviction is currently not implemented — tenants accumulate until restart.
// If memory pressure becomes an issue, add LRU eviction here without
// touching call sites.
type tenantAgentCache struct {
	mu     sync.Mutex
	agents map[string]*tenantAgentEntry
}

type tenantAgentEntry struct {
	once     sync.Once
	instance *AgentInstance
	err      error
}

func newTenantAgentCache() *tenantAgentCache {
	return &tenantAgentCache{agents: make(map[string]*tenantAgentEntry)}
}

// tenantKey derives a stable cache key from override values. We key on
// the resolved workspace path because that's the single source of truth
// for filesystem isolation: two tenants with the same workspace are the
// same tenant from picoclaw's point of view, even if MagicForm's
// stackId/conversationId look different.
func tenantKey(workspace, configDir string) string {
	return workspace + "::" + configDir
}

// resolveTenantAgent returns the AgentInstance for the given tenant
// overrides, building and caching it on first use. Returns nil and a nil
// error if no overrides are present (caller should fall back to the
// route's default agent).
//
// The build is best-effort: if any step fails (config load, provider
// construction, agent instantiation) the error is returned and cached so
// subsequent attempts for the same tenant fail fast instead of repeatedly
// trying. Restart the gateway to retry after fixing config.
func (al *AgentLoop) resolveTenantAgent(opts processOptions) (*AgentInstance, error) {
	if opts.WorkspaceOverride == "" && opts.ConfigDir == "" {
		return nil, nil
	}
	if opts.WorkspaceOverride == "" {
		// We require a workspace override because the AgentInstance's
		// filesystem tools must be rooted somewhere tenant-specific. A
		// config-dir override without a workspace would silently use the
		// default agent's workspace, which is the exact isolation bug we
		// exist to prevent.
		return nil, fmt.Errorf("tenant override rejected: workspace_override is required when config_dir is set")
	}

	if al.tenantAgents == nil {
		al.mu.Lock()
		if al.tenantAgents == nil {
			al.tenantAgents = newTenantAgentCache()
		}
		al.mu.Unlock()
	}

	key := tenantKey(opts.WorkspaceOverride, opts.ConfigDir)

	al.tenantAgents.mu.Lock()
	entry, ok := al.tenantAgents.agents[key]
	if !ok {
		entry = &tenantAgentEntry{}
		al.tenantAgents.agents[key] = entry
	}
	al.tenantAgents.mu.Unlock()

	entry.once.Do(func() {
		entry.instance, entry.err = al.buildTenantAgent(opts)
	})
	return entry.instance, entry.err
}

// buildTenantAgent provisions a fresh AgentInstance for a tenant. It:
//
//  1. Clones the base config so we don't mutate the gateway's shared cfg.
//  2. Loads <config_dir>/config.json if present and merges it onto the
//     clone (allows tenants to override provider, model, model_list, tool
//     enablement, session settings — exactly the surface
//     MergeWorkspaceConfig already permits).
//  3. Pins the workspace path on the cloned defaults so all downstream
//     workspace-aware code (filesystem tools, session store, context
//     builder) uses the tenant's workspace rather than the gateway's.
//  4. Resolves the effective LLM provider from the merged config so the
//     tenant's API key / model identifier is honored.
//  5. Constructs an AgentConfig that mirrors what NewAgentInstance
//     expects, then calls NewAgentInstance to do the heavy lifting.
//
// The returned instance is owned by the tenantAgentCache; caller must not
// close it.
func (al *AgentLoop) buildTenantAgent(opts processOptions) (*AgentInstance, error) {
	if al.cfg == nil {
		return nil, fmt.Errorf("tenant agent build: AgentLoop.cfg is nil")
	}

	cfgClone := al.cfg.Clone()

	// Apply workspace-local config overlay (provider keys, model_list, tool
	// enablement, etc.). MergeWorkspaceConfig is fork-owned and validates
	// that any workspace path inside the overlay still resolves within the
	// base config's WorkspaceRoot.
	if opts.ConfigDir != "" {
		wc, err := config.LoadWorkspaceConfig(opts.ConfigDir)
		if err != nil {
			return nil, fmt.Errorf("tenant agent build: load workspace config from %q: %w", opts.ConfigDir, err)
		}
		if err := cfgClone.MergeWorkspaceConfig(wc); err != nil {
			return nil, fmt.Errorf("tenant agent build: merge workspace config from %q: %w", opts.ConfigDir, err)
		}
	}

	// Pin the tenant workspace on the cloned defaults. This is the single
	// most important field for isolation: every subsequent tool and store
	// will read it.
	cfgClone.Agents.Defaults.Workspace = opts.WorkspaceOverride

	// Resolve provider from the merged config. We follow the same path
	// agent_init.go uses: GetModelConfig + the AgentLoop's providerFactory.
	modelName := cfgClone.Agents.Defaults.GetModelName()
	if modelName == "" {
		return nil, fmt.Errorf("tenant agent build: no model_name resolved for tenant %q", opts.WorkspaceOverride)
	}
	modelCfg, err := cfgClone.GetModelConfig(modelName)
	if err != nil {
		return nil, fmt.Errorf("tenant agent build: resolve model %q: %w", modelName, err)
	}
	if al.providerFactory == nil {
		return nil, fmt.Errorf(
			"tenant agent build: AgentLoop.providerFactory is nil; gateway not initialized correctly",
		)
	}
	provider, _, err := al.providerFactory(modelCfg)
	if err != nil {
		return nil, fmt.Errorf("tenant agent build: create provider from model %q: %w", modelName, err)
	}

	// Apply tenant tool/skill allowlists onto the cloned config so the
	// resulting AgentInstance is built with the right tool registry and
	// skill filter from the start. This is defense-in-depth complementing
	// the per-turn filtering already plumbed via processOptions.
	applyTenantToolAllowlist(&cfgClone.Tools, opts.AllowedTools)

	// Construct the agent config the same way upstream's implicit-main
	// path does (registry.go:33-36). We don't try to look up an entry from
	// agents.list because tenants are dynamic and don't have static
	// entries.
	agentCfg := &config.AgentConfig{
		ID:        deriveTenantAgentID(opts.WorkspaceOverride),
		Workspace: opts.WorkspaceOverride,
	}

	instance := NewAgentInstance(agentCfg, &cfgClone.Agents.Defaults, cfgClone, provider)
	if instance == nil {
		return nil, fmt.Errorf("tenant agent build: NewAgentInstance returned nil for %q", opts.WorkspaceOverride)
	}

	// Apply skills allowlist on the freshly built ContextBuilder. The
	// SkillsFilter mechanism already exists from Phase 1 in context.go.
	if len(opts.AllowedSkills) > 0 && instance.ContextBuilder != nil {
		instance.ContextBuilder.SetSkillsFilter(opts.AllowedSkills)
		instance.SkillsFilter = append([]string(nil), opts.AllowedSkills...)
	}

	// Best-effort bootstrap copy: when ConfigDir is set, seed the tenant
	// workspace with default skills/scripts/AGENT.md from configDir. Only
	// runs on first build (cache subsequent turns) so it doesn't churn on
	// every turn. Failures are logged but don't fail agent construction —
	// missing bootstrap files are recoverable; broken provider isn't.
	if opts.ConfigDir != "" {
		if err := provisionBootstrapFiles(opts.ConfigDir, opts.WorkspaceOverride); err != nil {
			logger.WarnCF("agent", "Tenant bootstrap copy failed (continuing with empty workspace)",
				map[string]any{
					"config_dir": opts.ConfigDir,
					"workspace":  opts.WorkspaceOverride,
					"error":      err.Error(),
				})
		}
	}

	logger.InfoCF("agent", "Provisioned tenant agent",
		map[string]any{
			"agent_id":  instance.ID,
			"workspace": instance.Workspace,
			"model":     instance.Model,
			"tools":     toolCount(instance),
		})

	return instance, nil
}

// applyTenantToolAllowlist mutates the cloned tools config so any tool
// not in the allowlist has its Enabled flag flipped to false before
// NewAgentInstance reads IsToolEnabled. Empty allowlist means no
// restriction (default behavior).
//
// Why mutate Enabled fields rather than maintaining a separate disabled
// list: ToolsConfig has one Enabled bool per tool struct; that's the
// only signal NewAgentInstance and IsToolEnabled consult. Keeping our
// override aligned with upstream's existing API means no sync conflicts.
func applyTenantToolAllowlist(toolsCfg *config.ToolsConfig, allowed []string) {
	if len(allowed) == 0 || toolsCfg == nil {
		return
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, t := range allowed {
		allowedSet[t] = struct{}{}
	}
	// Each tool gets its Enabled field flipped to false unless explicitly
	// in the allowlist. New upstream tools default to Enabled in their
	// struct's zero value, so missing one here errs on the side of
	// allowing it; the per-turn AllowedTools filter on processOptions is
	// defense-in-depth that still rejects calls.
	flip := func(enabled *bool, name string) {
		if _, ok := allowedSet[name]; !ok {
			*enabled = false
		}
	}
	flip(&toolsCfg.Web.Enabled, "web")
	flip(&toolsCfg.Cron.Enabled, "cron")
	flip(&toolsCfg.Exec.Enabled, "exec")
	flip(&toolsCfg.Skills.Enabled, "skills")
	flip(&toolsCfg.MediaCleanup.Enabled, "media_cleanup")
	flip(&toolsCfg.AppendFile.Enabled, "append_file")
	flip(&toolsCfg.EditFile.Enabled, "edit_file")
	flip(&toolsCfg.FindSkills.Enabled, "find_skills")
	flip(&toolsCfg.I2C.Enabled, "i2c")
	flip(&toolsCfg.InstallSkill.Enabled, "install_skill")
	flip(&toolsCfg.ListDir.Enabled, "list_dir")
	flip(&toolsCfg.Message.Enabled, "message")
	flip(&toolsCfg.ReadFile.Enabled, "read_file")
	flip(&toolsCfg.Serial.Enabled, "serial")
	flip(&toolsCfg.SendFile.Enabled, "send_file")
	flip(&toolsCfg.SendTTS.Enabled, "send_tts")
	flip(&toolsCfg.Spawn.Enabled, "spawn")
	flip(&toolsCfg.SpawnStatus.Enabled, "spawn_status")
	flip(&toolsCfg.SPI.Enabled, "spi")
	flip(&toolsCfg.Subagent.Enabled, "subagent")
	flip(&toolsCfg.WebFetch.Enabled, "web_fetch")
	flip(&toolsCfg.WriteFile.Enabled, "write_file")
	flip(&toolsCfg.MCP.Enabled, "mcp")
}

func toolCount(a *AgentInstance) int {
	if a == nil || a.Tools == nil {
		return 0
	}
	return len(a.Tools.ToProviderDefs())
}

// deriveTenantAgentID produces a stable, collision-resistant ID for a
// tenant agent. We hash the workspace path into a short suffix because:
//
//   - The last path segment (filepath.Base) collides badly when tenants
//     follow a "stack-id/workspace" or similar shared-leaf naming
//     convention — every tenant ends up with ID "workspace".
//   - The full path is too long for log readability and contains
//     characters NormalizeAgentID would collapse to dashes.
//
// Format: "tenant-<sha256[:8]>" — fits in NormalizeAgentID's 64-byte
// budget and is reliably unique for distinct workspace paths.
//
// Returns the form before NormalizeAgentID is applied by NewAgentInstance.
func deriveTenantAgentID(workspace string) string {
	if workspace == "" {
		return "tenant-unknown"
	}
	// fnv-style short hash via hash/crc32 would also work; sha256 is
	// already a transitive import and gives us collision odds far below
	// any plausible tenant count.
	sum := sha256.Sum256([]byte(filepath.Clean(workspace)))
	return "tenant-" + hex.EncodeToString(sum[:4])
}
