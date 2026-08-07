// PicoClaw - Agent-scoped construction of the allowlist-gated registries.
//
// The gate is applied here rather than in a package init() that replaces the
// upstream builders. A global override reaches every caller of the registries,
// including `picoclaw skills install` and the launcher's admin API, where the
// operator is the one asking and an empty allowlist would deny them by
// default. The untrusted input this gate exists to bound arrives on a tenant
// turn, so the gate belongs on the path that serves those turns.

package skills

import (
	"github.com/sipeed/picoclaw/pkg/config"
)

// NewAgentRegistryManagerFromToolsConfig builds the registry manager used by
// the agent loop, with every registry wrapped in its configured allowlist.
//
// Operator-driven callers (CLI, admin API) keep using
// NewRegistryManagerFromToolsConfig and upstream's ungated behavior.
func NewAgentRegistryManagerFromToolsConfig(cfg config.SkillsToolsConfig) *RegistryManager {
	return NewRegistryManagerFromConfig(RegistryConfig{
		Providers:             agentRegistryProvidersFromToolsConfig(cfg),
		MaxConcurrentSearches: cfg.MaxConcurrentSearches,
	})
}

// agentRegistryProvidersFromToolsConfig wraps each configured provider in the
// allowlist gate, reading the allow list from that registry's own params.
func agentRegistryProvidersFromToolsConfig(cfg config.SkillsToolsConfig) []RegistryProvider {
	registryConfigs := effectiveRegistryConfigsFromToolsConfig(cfg)
	providers := make([]RegistryProvider, 0, len(registryConfigs))
	for _, registryCfg := range registryConfigs {
		provider := buildRegistryProvider(registryCfg.Name, registryCfg)
		if provider == nil {
			continue
		}
		providers = append(providers, newAllowlistProvider(provider, registryCfg))
	}
	return providers
}
