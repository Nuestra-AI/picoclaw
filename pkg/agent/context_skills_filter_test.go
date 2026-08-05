package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

// writeSkill creates <workspace>/skills/<name>/SKILL.md so the skills loader
// discovers a workspace-level skill named <name>.
func writeSkill(t *testing.T, workspace, name, description string) {
	t.Helper()
	dir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillsFilterWildcardIncludesAllSkills pins the fork-only behavior that a
// ["*"] skills filter means "every skill".
//
// Upstream's cleanAllowedSet has no wildcard branch, so without the
// containsSkillWildcard shim in buildSystemPromptParts a ["*"] allowlist would
// match only a skill literally named "*" and silently produce an EMPTY skill
// catalog. That failure mode is invisible at runtime, hence this test.
// See NUESTRA_CUSTOMIZATIONS.md entry 2.
func TestSkillsFilterWildcardIncludesAllSkills(t *testing.T) {
	workspace := t.TempDir()
	writeSkill(t, workspace, "alpha", "first skill")
	writeSkill(t, workspace, "beta", "second skill")

	// Point the builtin skills dir at an empty directory. Without this the
	// loader defaults to <cwd>/skills and would treat the workspace skills as
	// builtin-shadowing, exercising a different code path than the one under
	// test (and picking up whatever skills the repo happens to ship).
	t.Setenv(config.EnvBuiltinSkills, t.TempDir())

	tests := []struct {
		name        string
		filter      []string
		wantAlpha   bool
		wantBeta    bool
		description string
	}{
		{
			name:        "wildcard includes all skills",
			filter:      []string{"*"},
			wantAlpha:   true,
			wantBeta:    true,
			description: `["*"] must behave as "all skills", not a literal name`,
		},
		{
			name:        "wildcard tolerates surrounding whitespace",
			filter:      []string{" * "},
			wantAlpha:   true,
			wantBeta:    true,
			description: "entries are trimmed to match cleanAllowedSet normalization",
		},
		{
			name:        "empty filter includes all skills",
			filter:      nil,
			wantAlpha:   true,
			wantBeta:    true,
			description: "no filter means no restriction",
		},
		{
			name:        "explicit allowlist filters to named skills",
			filter:      []string{"alpha"},
			wantAlpha:   true,
			wantBeta:    false,
			description: "non-wildcard entries still filter normally",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := NewContextBuilder(workspace)
			cb.SetSkillsFilter(tt.filter)

			prompt := cb.BuildSystemPrompt()

			if got := strings.Contains(prompt, "alpha"); got != tt.wantAlpha {
				t.Errorf("skill %q present = %v, want %v (%s)", "alpha", got, tt.wantAlpha, tt.description)
			}
			if got := strings.Contains(prompt, "beta"); got != tt.wantBeta {
				t.Errorf("skill %q present = %v, want %v (%s)", "beta", got, tt.wantBeta, tt.description)
			}
		})
	}
}

// TestContainsSkillWildcard covers the helper directly.
func TestContainsSkillWildcard(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		want    bool
	}{
		{name: "nil", allowed: nil, want: false},
		{name: "empty", allowed: []string{}, want: false},
		{name: "bare wildcard", allowed: []string{"*"}, want: true},
		{name: "padded wildcard", allowed: []string{"  *  "}, want: true},
		{name: "wildcard among names", allowed: []string{"alpha", "*"}, want: true},
		{name: "plain names only", allowed: []string{"alpha", "beta"}, want: false},
		{name: "glob is not a wildcard", allowed: []string{"alpha*"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsSkillWildcard(tt.allowed); got != tt.want {
				t.Errorf("containsSkillWildcard(%q) = %v, want %v", tt.allowed, got, tt.want)
			}
		})
	}
}
