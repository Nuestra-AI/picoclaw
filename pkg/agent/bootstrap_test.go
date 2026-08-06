package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapItemsCoverBothAgentFormats pins the shared list. Both agent
// formats must be present, or a workspace using the unlisted one ends up with
// no agent definition.
func TestBootstrapItemsCoverBothAgentFormats(t *testing.T) {
	want := map[string]bool{
		"AGENT.md":    false,
		"AGENTS.md":   false,
		"USER.md":     false,
		"SOUL.md":     false,
		"IDENTITY.md": false,
		"skills":      false,
		"scripts":     false,
	}
	for _, item := range bootstrapItems {
		if _, ok := want[item]; !ok {
			t.Errorf("unexpected bootstrap item %q", item)
			continue
		}
		want[item] = true
	}
	for item, seen := range want {
		if !seen {
			t.Errorf("bootstrap item %q missing from bootstrapItems", item)
		}
	}
}

// TestCopyBootstrapFilesCopiesStructuredAgentFormat covers the modern
// AGENT.md format plus the skills/ tree on the CLI path.
func TestCopyBootstrapFilesCopiesStructuredAgentFormat(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENT.md"), "---\nname: test\n---\nbody")
	writeFile(t, filepath.Join(src, "SOUL.md"), "soul")
	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir skills: %v", err)
	}
	writeFile(t, filepath.Join(src, "skills", "demo.md"), "skill")

	mustCopy(t, src, dst, false)

	assertFileContent(t, filepath.Join(dst, "AGENT.md"), "---\nname: test\n---\nbody")
	assertFileContent(t, filepath.Join(dst, "SOUL.md"), "soul")
	assertFileContent(t, filepath.Join(dst, "skills", "demo.md"), "skill")
}

// TestCopyBootstrapFilesCopiesLegacyAgentFormat covers the legacy AGENTS.md
// format.
func TestCopyBootstrapFilesCopiesLegacyAgentFormat(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENTS.md"), "legacy prompt")

	mustCopy(t, src, dst, false)

	assertFileContent(t, filepath.Join(dst, "AGENTS.md"), "legacy prompt")
}

// TestCopyBootstrapFilesIsIdempotent verifies an existing workspace file is
// not clobbered.
func TestCopyBootstrapFilesIsIdempotent(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENT.md"), "from config dir")
	writeFile(t, filepath.Join(dst, "AGENT.md"), "operator edit")

	mustCopy(t, src, dst, false)

	assertFileContent(t, filepath.Join(dst, "AGENT.md"), "operator edit")
}

// TestCopyBootstrapFilesSkipsMissingItems confirms a sparsely populated
// config dir is not an error — operators need not fill every slot.
func TestCopyBootstrapFilesSkipsMissingItems(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "USER.md"), "user")

	mustCopy(t, src, dst, false)

	assertFileContent(t, filepath.Join(dst, "USER.md"), "user")
	if _, err := os.Stat(filepath.Join(dst, "AGENT.md")); !os.IsNotExist(err) {
		t.Errorf("AGENT.md should not exist in dst, got err=%v", err)
	}
}

// TestCopyBootstrapFilesWithRelativeWorkspace covers a relative workspace,
// which the CLI hits whenever --workspace is omitted and
// agents.defaults.workspace is relative. Without absolute resolution the
// containment check fails for every item and nothing is copied.
func TestCopyBootstrapFilesWithRelativeWorkspace(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "AGENT.md"), "agent")

	// Run from a temp cwd so the relative workspace resolves somewhere safe.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	base := t.TempDir()
	if err := os.Chdir(base); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	// "./workspace" is the failing shape: filepath.Join cleans the leading
	// "./" off dst but not off the workspace it is compared against.
	mustCopy(t, src, "./workspace", false)

	assertFileContent(t, filepath.Join(base, "workspace", "AGENT.md"), "agent")
}

// TestProvisionContainmentRejectsSiblingPaths guards the containment check.
// Without a trailing separator the comparison is textual, so a workspace of
// /data/ws accepts the sibling /data/ws-evil. Not reachable through the
// hardcoded bootstrapItems, but the check must hold if items ever become
// operator-supplied.
func TestProvisionContainmentRejectsSiblingPaths(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "ws")

	cases := []struct {
		name string
		item string
		want bool // true = must be accepted as inside workspace
	}{
		{"plain file", "AGENT.md", true},
		{"nested dir", filepath.Join("skills", "demo.md"), true},
		{"sibling escape", filepath.Join("..", "ws-evil", "x"), false},
		{"parent escape", filepath.Join("..", "x"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(workspace, tc.item)
			got := strings.HasPrefix(dst, workspace+string(filepath.Separator))
			if got != tc.want {
				t.Errorf("item %q -> dst %q: contained=%v, want %v", tc.item, dst, got, tc.want)
			}
		})
	}
}

// TestCopyBootstrapFilesReportsSkippedItems checks that a diverging file is
// named in the skip list and an identical one is not.
func TestCopyBootstrapFilesReportsSkippedItems(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENT.md"), "from config dir")
	writeFile(t, filepath.Join(dst, "AGENT.md"), "operator edit")
	// USER.md is identical on both sides and must NOT be reported.
	writeFile(t, filepath.Join(src, "USER.md"), "same")
	writeFile(t, filepath.Join(dst, "USER.md"), "same")

	skipped := mustCopy(t, src, dst, false)

	if len(skipped) != 1 || skipped[0] != "AGENT.md" {
		t.Errorf("skipped = %v, want exactly [AGENT.md]", skipped)
	}
	assertFileContent(t, filepath.Join(dst, "AGENT.md"), "operator edit")
}

// TestCopyBootstrapFilesRefreshOverwrites verifies --refresh replaces a
// diverging file and reports nothing skipped.
func TestCopyBootstrapFilesRefreshOverwrites(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENT.md"), "from config dir")
	writeFile(t, filepath.Join(dst, "AGENT.md"), "operator edit")

	skipped := mustCopy(t, src, dst, true)

	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none with refresh", skipped)
	}
	assertFileContent(t, filepath.Join(dst, "AGENT.md"), "from config dir")
}

// TestCopyBootstrapFilesReportsSkippedInsideDirs confirms skip reporting
// reaches inside skills/ and scripts/, with workspace-relative paths.
func TestCopyBootstrapFilesReportsSkippedInsideDirs(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir src skills: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir dst skills: %v", err)
	}
	writeFile(t, filepath.Join(src, "skills", "demo.md"), "new skill")
	writeFile(t, filepath.Join(dst, "skills", "demo.md"), "edited skill")

	skipped := mustCopy(t, src, dst, false)

	want := filepath.Join("skills", "demo.md")
	if len(skipped) != 1 || skipped[0] != want {
		t.Errorf("skipped = %v, want exactly [%s]", skipped, want)
	}
}

// TestCopyBootstrapFilesReturnsError confirms failures reach the caller
// instead of being swallowed into a log line.
func TestCopyBootstrapFilesReturnsError(t *testing.T) {
	if _, err := CopyBootstrapFiles("", t.TempDir(), false); err == nil {
		t.Error("expected an error for an empty config dir, got nil")
	}
}

// mustCopy runs CopyBootstrapFiles and fails the test on error, returning the
// skipped list.
func mustCopy(t *testing.T, src, dst string, refresh bool) []string {
	t.Helper()
	skipped, err := CopyBootstrapFiles(src, dst, refresh)
	if err != nil {
		t.Fatalf("CopyBootstrapFiles(%q, %q, %v): %v", src, dst, refresh, err)
	}
	return skipped
}

// TestCopyBootstrapFilesRejectsSymlinkedDir covers the escape the string
// prefix check cannot see: workspace/skills is a symlink out of the
// workspace, so MkdirAll and the file write would follow it. The agent can
// plant such a link itself whenever the exec tool is enabled.
func TestCopyBootstrapFilesRejectsSymlinkedDir(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(src, "skills"), 0o755); err != nil {
		t.Fatalf("mkdir src skills: %v", err)
	}
	writeFile(t, filepath.Join(src, "skills", "demo.md"), "skill")

	link := filepath.Join(dst, "skills")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	if _, err := CopyBootstrapFiles(src, dst, false); err == nil {
		t.Error("expected an error writing through a symlinked directory, got nil")
	}
	if _, err := os.Stat(filepath.Join(outside, "demo.md")); err == nil {
		t.Error("wrote through the symlink to a path outside the workspace")
	}
}

// TestCopyBootstrapFilesRejectsSymlinkedFile covers the same escape when the
// destination file itself is the link rather than a parent directory.
func TestCopyBootstrapFilesRejectsSymlinkedFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	outside := t.TempDir()

	writeFile(t, filepath.Join(src, "AGENT.md"), "agent")
	target := filepath.Join(outside, "captured.md")
	writeFile(t, target, "original")

	if err := os.Symlink(target, filepath.Join(dst, "AGENT.md")); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}

	if _, err := CopyBootstrapFiles(src, dst, true); err == nil {
		t.Error("expected an error writing through a symlinked file, got nil")
	}
	assertFileContent(t, target, "original")
}

// fakeSymlinkInfo reports a path as a symlink so the traversal can be tested
// where creating real symlinks needs elevation (Windows).
type fakeSymlinkInfo struct{ os.FileInfo }

func (fakeSymlinkInfo) Mode() os.FileMode { return os.ModeSymlink }

// TestCheckNoSymlinkedParent exercises the symlink traversal directly with a
// stubbed lstat, so it runs on every platform rather than skipping where
// symlink creation is privileged. The real-symlink tests above cover the
// integration on platforms that permit it.
func TestCheckNoSymlinkedParent(t *testing.T) {
	ws := filepath.FromSlash("/data/ws")

	cases := []struct {
		name      string
		dst       string
		symlinks  map[string]bool // paths reported as symlinks
		missing   map[string]bool // paths reported as not existing
		wantError bool
	}{
		{
			name: "no symlinks",
			dst:  filepath.Join(ws, "skills", "demo.md"),
		},
		{
			name:      "symlinked parent dir",
			dst:       filepath.Join(ws, "skills", "demo.md"),
			symlinks:  map[string]bool{filepath.Join(ws, "skills"): true},
			wantError: true,
		},
		{
			name:      "symlinked destination file",
			dst:       filepath.Join(ws, "AGENT.md"),
			symlinks:  map[string]bool{filepath.Join(ws, "AGENT.md"): true},
			wantError: true,
		},
		{
			name:      "symlink deep in the tree",
			dst:       filepath.Join(ws, "skills", "nested", "demo.md"),
			symlinks:  map[string]bool{filepath.Join(ws, "skills", "nested"): true},
			wantError: true,
		},
		{
			name:    "missing component stops the walk",
			dst:     filepath.Join(ws, "skills", "demo.md"),
			missing: map[string]bool{filepath.Join(ws, "skills"): true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Non-symlink, existing entries need a real FileInfo; the test
			// binary is guaranteed to exist and is not a symlink.
			self, statErr := os.Stat(os.Args[0])
			if statErr != nil {
				t.Fatalf("stat self: %v", statErr)
			}
			lstat := func(path string) (os.FileInfo, error) {
				if tc.missing[path] {
					return nil, os.ErrNotExist
				}
				if tc.symlinks[path] {
					return fakeSymlinkInfo{}, nil
				}
				return self, nil
			}

			err := checkNoSymlinkedParent(ws, tc.dst, lstat)
			if tc.wantError && err == nil {
				t.Error("expected an error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestSameFileContentStreaming exercises the chunked comparison across the
// 32 KiB buffer boundary, including a mismatch in the final partial chunk.
func TestSameFileContentStreaming(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", 32*1024+512)

	cases := []struct {
		name     string
		a, b     string
		wantSame bool
	}{
		{"identical multi-chunk", big, big, true},
		{"differ in final chunk", big, big[:len(big)-1] + "b", false},
		{"differ in first chunk", big, "b" + big[1:], false},
		{"both empty", "", "", true},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := filepath.Join(dir, fmt.Sprintf("a%d", i))
			b := filepath.Join(dir, fmt.Sprintf("b%d", i))
			writeFile(t, a, tc.a)
			writeFile(t, b, tc.b)

			got, err := sameFileContent(a, b)
			if err != nil {
				t.Fatalf("sameFileContent: %v", err)
			}
			if got != tc.wantSame {
				t.Errorf("sameFileContent = %v, want %v", got, tc.wantSame)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s: got %q, want %q", path, string(got), want)
	}
}
