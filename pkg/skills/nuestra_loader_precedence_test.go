package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates root/<name>/SKILL.md with a body that identifies which
// root it came from.
func writeSkill(t *testing.T, root, name, marker string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := "---\nname: " + name + "\ndescription: test skill\n---\n\n" + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

// The precedence documented in deploy/README.md: builtin always wins, then
// workspace, then global. The builtin tier is a veto rather than a
// preference -- a tenant must not be able to redefine a builtin skill name.
func TestSkillRootPrecedenceIsDocumentedOrder(t *testing.T) {
	workspace := t.TempDir()
	global := t.TempDir()
	builtin := t.TempDir()

	// One name present in all three roots.
	writeSkill(t, filepath.Join(workspace, "skills"), "shared", "FROM-WORKSPACE")
	writeSkill(t, global, "shared", "FROM-GLOBAL")
	writeSkill(t, builtin, "shared", "FROM-BUILTIN")

	// A name in workspace and global only.
	writeSkill(t, filepath.Join(workspace, "skills"), "overlaid", "FROM-WORKSPACE")
	writeSkill(t, global, "overlaid", "FROM-GLOBAL")

	// A name in global only.
	writeSkill(t, global, "global-only", "FROM-GLOBAL")

	loader := NewSkillsLoader(workspace, global, builtin)

	cases := []struct {
		name string
		want string
		why  string
	}{
		{"shared", "FROM-BUILTIN", "builtin must veto both other roots"},
		{"overlaid", "FROM-WORKSPACE", "workspace must win over global"},
		{"global-only", "FROM-GLOBAL", "global is the last resort"},
	}
	for _, tc := range cases {
		content := loader.LoadSkillsForContext([]string{tc.name})
		if !strings.Contains(content, tc.want) {
			t.Errorf("skill %q: %s -- got %q", tc.name, tc.why, content)
		}
	}
}

// A skill directory without SKILL.md is not a skill. Operators seeding
// config_dir/skills/ rely on this being an inert no-op rather than an error.
func TestSkillDirectoryWithoutSkillMarkdownIsIgnored(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, "skills", "not-a-skill")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loader := NewSkillsLoader(workspace, "", "")
	for _, s := range loader.ListSkills() {
		if s.Name == "not-a-skill" {
			t.Fatal("a directory without SKILL.md was listed as a skill")
		}
	}
}

// Discovery is one directory level deep; a nested layout is invisible.
func TestNestedSkillDirectoriesAreNotDiscovered(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, filepath.Join(workspace, "skills", "category"), "nested", "FROM-NESTED")

	loader := NewSkillsLoader(workspace, "", "")
	for _, s := range loader.ListSkills() {
		if s.Name == "nested" {
			t.Fatal("a skill nested two levels deep was discovered")
		}
	}
}
