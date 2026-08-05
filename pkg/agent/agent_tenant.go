// PicoClaw - Multi-tenancy overrides for the agent loop.
//
// This file is one of the Nuestra fork's primary customizations on top of
// upstream. It extracts per-message tenant hints (workspace, config dir,
// tool/skill allowlists) from InboundMessage.Context.Raw and validates them
// against the workspace_root security boundary configured in agents.defaults.
//
// This layer is channel-agnostic: it reads generic Context.Raw keys and does
// not know or care which channel produced them. The magicform channel is
// currently the only producer, but any channel can populate the same keys.
//
// Keeping the logic in its own file (rather than scattered through
// agent_message.go) makes upstream syncs easier: most upstream changes won't
// touch this file, and when they do the conflict surface is small and obvious.
//
// PHASE 1 (shipped): plumb the override fields onto processOptions so the
// agent loop has them available. The fields ride on processOptions and
// downstream callers (pipeline_llm, turn_state, etc.) read them.
//
// PHASE 2 (shipped): each tenant resolves to an isolated AgentInstance with
// its own workspace, session store, ContextBuilder, Tools and Provider. See
// agent_tenant_registry.go for the cache and construction path; the per-turn
// allowlists here remain as defense-in-depth.

package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/pathutil"
)

// Inbound Context.Raw keys carrying tenant hints. Channels (e.g. magicform)
// populate these; the agent loop reads them here.
const (
	rawKeyWorkspaceOverride = "workspace_override"
	rawKeyConfigDir         = "config_dir"
	rawKeyAllowedTools      = "allowed_tools"
	rawKeyAllowedSkills     = "allowed_skills"
)

// extractTenantOverrides reads multi-tenancy hints from msg.Context.Raw, and,
// when workspace_root is configured, validates that workspace_override and
// config_dir resolve inside it. Returns nil and a zero-valued options patch
// when no hints are present. Returns an error if a hint escapes workspace_root.
func (al *AgentLoop) extractTenantOverrides(msg bus.InboundMessage) (tenantOverrides, error) {
	out := tenantOverrides{}
	if msg.Context.Raw == nil {
		return out, nil
	}

	out.workspace = strings.TrimSpace(msg.Context.Raw[rawKeyWorkspaceOverride])
	out.configDir = strings.TrimSpace(msg.Context.Raw[rawKeyConfigDir])
	out.allowedTools = splitCSV(msg.Context.Raw[rawKeyAllowedTools])
	out.allowedSkills = splitCSV(msg.Context.Raw[rawKeyAllowedSkills])

	if out.workspace == "" && out.configDir == "" {
		return out, nil
	}

	root := al.cfg.Agents.Defaults.WorkspaceRoot
	if root == "" {
		// No boundary configured; fail closed rather than allow unbounded paths.
		return tenantOverrides{}, errors.New(
			"tenant override rejected: agents.defaults.workspace_root must be set " +
				"to validate workspace_override / config_dir hints",
		)
	}

	if out.workspace != "" {
		resolved, err := pathutil.ResolveWorkspacePath(root, out.workspace)
		if err != nil {
			return tenantOverrides{}, fmt.Errorf("workspace_override rejected: %w", err)
		}
		out.workspace = resolved
	}
	if out.configDir != "" {
		resolved, err := pathutil.ResolveWorkspacePath(root, out.configDir)
		if err != nil {
			return tenantOverrides{}, fmt.Errorf("config_dir rejected: %w", err)
		}
		out.configDir = resolved
	}
	return out, nil
}

// tenantOverrides bundles the hints that need to be threaded onto
// processOptions. Kept as a separate value type so the extractor can return
// "no overrides" cheaply, and so future fields (effSessions, effProvider, …)
// can be added in one place.
type tenantOverrides struct {
	workspace     string
	configDir     string
	allowedTools  []string
	allowedSkills []string
}

// applyTo copies the override fields onto processOptions. Caller must have
// already validated via extractTenantOverrides.
func (o tenantOverrides) applyTo(opts *processOptions) {
	if o.workspace != "" {
		opts.WorkspaceOverride = o.workspace
	}
	if o.configDir != "" {
		opts.ConfigDir = o.configDir
	}
	if len(o.allowedTools) > 0 {
		opts.AllowedTools = o.allowedTools
	}
	if len(o.allowedSkills) > 0 {
		opts.AllowedSkills = o.allowedSkills
	}
}

// logIfPresent emits a single info log line summarizing the override surface
// for a turn, so operators can see tenant routing at a glance.
func (o tenantOverrides) logIfPresent(sessionKey string) {
	if o.workspace == "" && o.configDir == "" && len(o.allowedTools) == 0 && len(o.allowedSkills) == 0 {
		return
	}
	logger.InfoCF("agent", "Tenant override applied",
		map[string]any{
			"session_key":    sessionKey,
			"workspace":      o.workspace,
			"config_dir":     o.configDir,
			"allowed_tools":  o.allowedTools,
			"allowed_skills": o.allowedSkills,
		})
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
