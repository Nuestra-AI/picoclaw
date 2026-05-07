package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// newTenantTestLoop spins up a minimal AgentLoop suitable for tenant
// registry tests: cfg.WorkspaceRoot is set to t.TempDir() so all tenant
// hints resolve inside it, and providerFactory returns a stub so we don't
// hit real model APIs.
func newTenantTestLoop(t *testing.T) (*AgentLoop, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.WorkspaceRoot = root
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "default")
	cfg.Agents.Defaults.ModelName = "stub-model"
	cfg.Agents.Defaults.MaxTokens = 4096
	cfg.Agents.Defaults.MaxToolIterations = 10
	// A bare-bones model_list entry so GetModelConfig succeeds.
	cfg.ModelList = config.SecureModelList{{
		ModelName: "stub-model",
		Provider:  "stub",
		Model:     "stub-model",
	}}

	provider := &simpleMockProvider{response: "ok"}
	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	al.providerFactory = func(mc *config.ModelConfig) (providers.LLMProvider, string, error) {
		return provider, "stub-model", nil
	}
	return al, root
}

// resolvePathInsideRoot returns an absolute path inside root (unique per
// suffix) and ensures the directory exists, mirroring what
// pathutil.ResolveWorkspacePath would produce for an inbound override.
func resolvePathInsideRoot(t *testing.T, root, suffix string) string {
	t.Helper()
	p := filepath.Join(root, suffix)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", p, err)
	}
	return p
}

func TestResolveTenantAgent_NoOverridesReturnsNil(t *testing.T) {
	al, _ := newTenantTestLoop(t)
	got, err := al.resolveTenantAgent(processOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil agent for empty options, got %v", got)
	}
}

func TestResolveTenantAgent_ConfigDirWithoutWorkspaceRejected(t *testing.T) {
	al, root := newTenantTestLoop(t)
	cfgDir := resolvePathInsideRoot(t, root, "tenant-a/config")
	_, err := al.resolveTenantAgent(processOptions{ConfigDir: cfgDir})
	if err == nil {
		t.Fatalf("expected error when config_dir is set without workspace, got nil")
	}
	if !strings.Contains(err.Error(), "workspace_override is required") {
		t.Fatalf("error message %q does not mention workspace requirement", err.Error())
	}
}

func TestResolveTenantAgent_BuildsIsolatedAgents(t *testing.T) {
	al, root := newTenantTestLoop(t)
	wsA := resolvePathInsideRoot(t, root, "tenant-a/workspace")
	wsB := resolvePathInsideRoot(t, root, "tenant-b/workspace")

	tenantA, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: wsA})
	if err != nil {
		t.Fatalf("build tenant A: %v", err)
	}
	tenantB, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: wsB})
	if err != nil {
		t.Fatalf("build tenant B: %v", err)
	}

	// Distinct AgentInstance pointers.
	if tenantA == tenantB {
		t.Fatalf("expected distinct tenant agents, both pointed at %p", tenantA)
	}
	// Distinct workspaces — the load-bearing isolation property.
	if tenantA.Workspace == tenantB.Workspace {
		t.Fatalf("tenants share workspace %q (isolation broken)", tenantA.Workspace)
	}
	if tenantA.Workspace != wsA {
		t.Errorf("tenantA.Workspace = %q, want %q", tenantA.Workspace, wsA)
	}
	if tenantB.Workspace != wsB {
		t.Errorf("tenantB.Workspace = %q, want %q", tenantB.Workspace, wsB)
	}
	// Distinct session stores — the second isolation property.
	if tenantA.Sessions == tenantB.Sessions {
		t.Fatalf("tenants share Sessions store (isolation broken)")
	}
	// Distinct context builders.
	if tenantA.ContextBuilder == tenantB.ContextBuilder {
		t.Fatalf("tenants share ContextBuilder (isolation broken)")
	}
	// Tenant-derived agent IDs are distinct even when the workspace path
	// shares a leaf name like "tenant-a/workspace" + "tenant-b/workspace"
	// (a real-world MagicForm naming pattern).
	if !strings.HasPrefix(tenantA.ID, "tenant-") {
		t.Errorf("tenantA.ID = %q, expected tenant- prefix", tenantA.ID)
	}
	if tenantA.ID == tenantB.ID {
		t.Errorf("tenant IDs collided: both = %q (deriveTenantAgentID must be path-unique)", tenantA.ID)
	}
}

func TestResolveTenantAgent_CachesByKey(t *testing.T) {
	al, root := newTenantTestLoop(t)
	ws := resolvePathInsideRoot(t, root, "tenant-cache/workspace")

	first, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: ws})
	if err != nil {
		t.Fatalf("build first: %v", err)
	}
	second, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: ws})
	if err != nil {
		t.Fatalf("build second: %v", err)
	}
	if first != second {
		t.Fatalf("expected same cached AgentInstance, got %p vs %p", first, second)
	}
}

func TestResolveTenantAgent_DifferentConfigDirsAreDifferentTenants(t *testing.T) {
	al, root := newTenantTestLoop(t)
	ws := resolvePathInsideRoot(t, root, "tenant-multi/workspace")
	cfg1 := resolvePathInsideRoot(t, root, "tenant-multi/cfg-1")
	cfg2 := resolvePathInsideRoot(t, root, "tenant-multi/cfg-2")

	a, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: ws, ConfigDir: cfg1})
	if err != nil {
		t.Fatalf("tenant cfg1: %v", err)
	}
	b, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: ws, ConfigDir: cfg2})
	if err != nil {
		t.Fatalf("tenant cfg2: %v", err)
	}
	if a == b {
		t.Fatalf("expected distinct agents for different config_dirs, both = %p", a)
	}
}

func TestResolveTenantAgent_ConcurrentBuildsForSameTenantCoalesce(t *testing.T) {
	al, root := newTenantTestLoop(t)
	ws := resolvePathInsideRoot(t, root, "tenant-concurrent/workspace")

	const N = 16
	var wg sync.WaitGroup
	results := make([]*AgentInstance, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = al.resolveTenantAgent(processOptions{WorkspaceOverride: ws})
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("goroutine %d errored: %v", i, e)
		}
	}
	first := results[0]
	for i, r := range results {
		if r != first {
			t.Fatalf("goroutine %d got %p, want %p (cache should coalesce)", i, r, first)
		}
	}
}

func TestResolveTenantAgent_ConcurrentDistinctTenantsAreIsolated(t *testing.T) {
	al, root := newTenantTestLoop(t)

	const N = 8
	workspaces := make([]string, N)
	for i := 0; i < N; i++ {
		workspaces[i] = resolvePathInsideRoot(t, root, fmt.Sprintf("tenant-%d/workspace", i))
	}

	var wg sync.WaitGroup
	results := make([]*AgentInstance, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := al.resolveTenantAgent(processOptions{WorkspaceOverride: workspaces[i]})
			if err != nil {
				t.Errorf("tenant %d build: %v", i, err)
				return
			}
			results[i] = a
		}(i)
	}
	wg.Wait()

	// Every concurrent tenant got its own AgentInstance and its own
	// workspace. None of them collided with another.
	seen := make(map[*AgentInstance]int, N)
	wsSeen := make(map[string]int, N)
	for i, a := range results {
		if a == nil {
			t.Fatalf("tenant %d nil result", i)
		}
		if prev, ok := seen[a]; ok {
			t.Errorf("tenants %d and %d share AgentInstance %p", prev, i, a)
		}
		seen[a] = i
		if prev, ok := wsSeen[a.Workspace]; ok {
			t.Errorf("tenants %d and %d share workspace %q", prev, i, a.Workspace)
		}
		wsSeen[a.Workspace] = i
	}
}

func TestApplyTenantToolAllowlist(t *testing.T) {
	cfg := config.DefaultConfig()
	// Pre-enable a few tools so we can verify the disabling path.
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Cron.Enabled = true
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.WriteFile.Enabled = true

	applyTenantToolAllowlist(&cfg.Tools, []string{"exec", "write_file"})

	if cfg.Tools.Web.Enabled {
		t.Errorf("web should be disabled after allowlist excludes it")
	}
	if cfg.Tools.Cron.Enabled {
		t.Errorf("cron should be disabled after allowlist excludes it")
	}
	if !cfg.Tools.Exec.Enabled {
		t.Errorf("exec should remain enabled (in allowlist)")
	}
	if !cfg.Tools.WriteFile.Enabled {
		t.Errorf("write_file should remain enabled (in allowlist)")
	}
}

func TestApplyTenantToolAllowlist_EmptyIsNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = true
	applyTenantToolAllowlist(&cfg.Tools, nil)
	if !cfg.Tools.Exec.Enabled {
		t.Errorf("empty allowlist should not disable anything")
	}
}

func TestProvisionBootstrapFiles_CopiesFiles(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "cfg")
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(filepath.Join(configDir, "skills", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "AGENT.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(configDir, "skills", "demo", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("a skill"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := provisionBootstrapFiles(configDir, workspace); err != nil {
		t.Fatalf("provisionBootstrapFiles: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(workspace, "AGENT.md")); err != nil || string(got) != "hello" {
		t.Errorf("AGENT.md not copied correctly: got=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "skills", "demo", "SKILL.md")); err != nil || string(got) != "a skill" {
		t.Errorf("skills/demo/SKILL.md not copied correctly: got=%q err=%v", got, err)
	}
}

func TestProvisionBootstrapFiles_DoesNotOverwriteExisting(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "cfg")
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "AGENT.md"), []byte("from-cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "AGENT.md"), []byte("operator-edit"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := provisionBootstrapFiles(configDir, workspace); err != nil {
		t.Fatalf("provisionBootstrapFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(workspace, "AGENT.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "operator-edit" {
		t.Errorf("operator's edit was overwritten: got %q, want %q", got, "operator-edit")
	}
}

func TestProvisionBootstrapFiles_MissingItemsAreSkipped(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "cfg")
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// configDir has none of the bootstrap items; provision should still
	// succeed and create the workspace dir.
	if err := provisionBootstrapFiles(configDir, workspace); err != nil {
		t.Fatalf("provisionBootstrapFiles with empty configDir: %v", err)
	}
	if _, err := os.Stat(workspace); err != nil {
		t.Errorf("workspace was not created: %v", err)
	}
}
