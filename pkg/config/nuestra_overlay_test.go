package config

import "testing"

// TestOverlayCannotGrantReadOutsideWorkspace pins the read boundary to the
// base config. AllowReadOutsideWorkspace is the only input that clears
// readRestrict in pkg/agent/instance.go, and tenant workspaces are siblings
// under workspace_root, so an overlay that could set it would let one tenant
// read another's files.
func TestOverlayCannotGrantReadOutsideWorkspace(t *testing.T) {
	base := &AgentDefaults{
		WorkspaceRoot:             "/data/workspaces",
		RestrictToWorkspace:       true,
		AllowReadOutsideWorkspace: false,
	}
	overlay := &AgentDefaults{AllowReadOutsideWorkspace: true}

	if err := mergeAgentDefaults(base, overlay); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if base.AllowReadOutsideWorkspace {
		t.Error("overlay escalated allow_read_outside_workspace; the base config must own the read boundary")
	}
}

// TestOverlayKeepsBaseAllowReadOutsideWorkspace confirms the field is ignored
// rather than forced off: a deployment that sets it globally keeps it.
func TestOverlayKeepsBaseAllowReadOutsideWorkspace(t *testing.T) {
	base := &AgentDefaults{AllowReadOutsideWorkspace: true}
	overlay := &AgentDefaults{ModelName: "tenant-main"}

	if err := mergeAgentDefaults(base, overlay); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !base.AllowReadOutsideWorkspace {
		t.Error("base allow_read_outside_workspace was cleared by an unrelated overlay")
	}
	if base.ModelName != "tenant-main" {
		t.Errorf("ModelName = %q, want tenant-main", base.ModelName)
	}
}

// TestOverlayCanOnlyTightenRestrictToWorkspace documents the asymmetry with
// the flag above: RestrictToWorkspace is positive, so merging only on true
// lets an overlay tighten but never loosen.
func TestOverlayCanOnlyTightenRestrictToWorkspace(t *testing.T) {
	t.Run("overlay can tighten", func(t *testing.T) {
		base := &AgentDefaults{RestrictToWorkspace: false}
		if err := mergeAgentDefaults(base, &AgentDefaults{RestrictToWorkspace: true}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if !base.RestrictToWorkspace {
			t.Error("overlay could not tighten restrict_to_workspace")
		}
	})

	t.Run("overlay cannot loosen", func(t *testing.T) {
		base := &AgentDefaults{RestrictToWorkspace: true}
		if err := mergeAgentDefaults(base, &AgentDefaults{RestrictToWorkspace: false}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if !base.RestrictToWorkspace {
			t.Error("overlay loosened restrict_to_workspace")
		}
	})
}

// TestOverlayCannotSetWorkspaceRoot pins the existing boundary rule alongside
// the read boundary, so both stay covered by one test file.
func TestOverlayCannotSetWorkspaceRoot(t *testing.T) {
	base := &AgentDefaults{WorkspaceRoot: "/data/workspaces"}
	overlay := &AgentDefaults{WorkspaceRoot: "/etc"}

	if err := mergeAgentDefaults(base, overlay); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if base.WorkspaceRoot != "/data/workspaces" {
		t.Errorf("WorkspaceRoot = %q, want the base value to survive", base.WorkspaceRoot)
	}
}
