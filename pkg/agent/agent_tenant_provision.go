// PicoClaw - Bootstrap-file copy for first-time tenant workspaces.
//
// When a channel sends an inbound message with config_dir + workspace
// hints and that tenant's workspace doesn't yet exist, we copy a small
// set of bootstrap files (AGENT.md, USER.md, SOUL.md, IDENTITY.md, the
// skills/ tree, and scripts/) from the operator-managed config_dir into
// the workspace. This gives the tenant an immediately-usable agent on
// first turn, without requiring the operator to seed every workspace by
// hand.
//
// Idempotent: existing files in the destination are left alone. We only
// write what isn't there. This means re-provisioning is safe and that an
// operator-edit of the workspace will not be silently overwritten by a
// stale config_dir copy on the next turn.
//
// Path safety: every destination path is resolved with filepath.Join
// against the workspace root and rejected if it escapes. Same defense as
// pathutil.ResolveWorkspacePath uses for inbound webhook hints.

package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bootstrapItems lists the files and directories copied from configDir into
// a tenant workspace. Items are relative to configDir; missing items are
// skipped.
//
// Shared by both provisioning paths: the gateway (provisionBootstrapFiles)
// and the CLI (CopyBootstrapFiles), so one config_dir yields one workspace
// layout either way.
//
// Both AGENT.md and AGENTS.md are listed because both are live formats;
// loadAgentDefinition prefers AGENT.md and falls back to AGENTS.md.
var bootstrapItems = []string{
	"AGENT.md",
	"AGENTS.md",
	"USER.md",
	"SOUL.md",
	"IDENTITY.md",
	"skills",
	"scripts",
}

// provisionBootstrapFiles copies bootstrapItems from configDir into
// workspace, leaving existing files alone. Returns the first error
// encountered; partial copies remain so the caller can decide whether the
// tenant agent is usable.
func provisionBootstrapFiles(configDir, workspace string) error {
	_, err := provisionBootstrap(configDir, workspace, false)
	return err
}

// provisionBootstrap copies bootstrapItems from configDir into workspace.
// When refresh is true, existing files whose content differs from the source
// are overwritten; otherwise they are preserved and returned as skipped
// (workspace-relative). Identical files are never reported.
//
// Both paths are made absolute here rather than trusting the caller: the CLI
// does not always have a validated workspace, and a relative one would fail
// the containment check for every item and silently copy nothing.
//
// A differing file cannot be told apart from a deliberate operator edit,
// which is why the default preserves and reports rather than overwrites.
func provisionBootstrap(configDir, workspace string, refresh bool) ([]string, error) {
	if configDir == "" || workspace == "" {
		return nil, errors.New("provisionBootstrap: configDir and workspace must be non-empty")
	}
	configDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}
	var skipped []string
	for _, item := range bootstrapItems {
		src := filepath.Join(configDir, item)
		dst := filepath.Join(workspace, item)

		// Reject anything resolving outside the workspace. The trailing
		// separator is required: without it a workspace of /data/ws accepts
		// the sibling /data/ws-evil/x. Matches pathutil.ResolveWorkspacePath.
		if !strings.HasPrefix(dst, workspace+string(filepath.Separator)) {
			return nil, fmt.Errorf("bootstrap item %q resolves outside workspace", item)
		}

		info, err := os.Stat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", src, err)
		}

		if info.IsDir() {
			dirSkipped, err := copyDir(workspace, src, dst, refresh)
			if err != nil {
				return nil, fmt.Errorf("copy dir %q: %w", item, err)
			}
			for _, rel := range dirSkipped {
				skipped = append(skipped, filepath.Join(item, rel))
			}
		} else {
			wasSkipped, err := copyFile(workspace, src, dst, info.Mode().Perm(), refresh)
			if err != nil {
				return nil, fmt.Errorf("copy file %q: %w", item, err)
			}
			if wasSkipped {
				skipped = append(skipped, item)
			}
		}
	}
	return skipped, nil
}

// copyDir walks src and mirrors files into dst, never deleting files present
// in dst but not src (dst is the operator's editable surface). Returns the
// src-relative paths left untouched because they exist with differing
// content.
func copyDir(workspace, src, dst string, refresh bool) ([]string, error) {
	var skipped []string
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			// MkdirAll follows an existing symlinked component, so check
			// before creating.
			if err := assertNoSymlinkedParent(workspace, target); err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		// Skip symlinks: we don't want to follow operator-set links that
		// could point outside the workspace, nor recreate them in a place
		// where they'd resolve differently.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		wasSkipped, err := copyFile(workspace, path, target, info.Mode().Perm(), refresh)
		if err != nil {
			return err
		}
		if wasSkipped {
			skipped = append(skipped, rel)
		}
		return nil
	})
	return skipped, err
}

// copyFile copies src→dst. An existing dst is preserved unless refresh is
// set. Returns true when an existing dst was preserved despite differing
// from src; identical files report false. Preserves the source mode (within
// umask); 0o600 is the fallback floor.
//
// workspace bounds the write: no component of dst may be a symlink, so a
// link planted inside the workspace cannot redirect the write outside it.
func copyFile(workspace, src, dst string, mode os.FileMode, refresh bool) (bool, error) {
	if err := assertNoSymlinkedParent(workspace, dst); err != nil {
		return false, err
	}
	if _, err := os.Stat(dst); err == nil {
		same, cmpErr := sameFileContent(src, dst)
		if cmpErr != nil {
			return false, cmpErr
		}
		if same {
			return false, nil
		}
		if !refresh {
			return true, nil
		}
		if err := os.Remove(dst); err != nil {
			return false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()

	if mode == 0 {
		mode = 0o600
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return false, err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return false, err
	}
	return false, out.Sync()
}

// assertNoSymlinkedParent verifies that no path component between workspace
// and dst is a symlink. The prefix check in provisionBootstrap compares
// strings, so it cannot see that workspace/skills is a link to /etc; MkdirAll
// and file creation would then follow that link and write outside the
// workspace.
//
// The agent can create such a link itself when the exec tool is enabled, so
// this is checked on every item rather than trusted from provisioning time.
// Lstat is used deliberately: Stat would resolve the link and report the
// target's type.
func assertNoSymlinkedParent(workspace, dst string) error {
	return checkNoSymlinkedParent(workspace, dst, os.Lstat)
}

// checkNoSymlinkedParent is assertNoSymlinkedParent with the stat call
// injected. Symlink creation needs elevation on Windows, so tests substitute
// a stub here to exercise the traversal on any platform.
func checkNoSymlinkedParent(workspace, dst string, lstat func(string) (os.FileInfo, error)) error {
	rel, err := filepath.Rel(workspace, dst)
	if err != nil {
		return err
	}
	current := workspace
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// Nothing exists from here down, so nothing can be followed.
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlink %q inside workspace", current)
		}
	}
	return nil
}

// sameFileContent reports whether src and dst hold identical bytes. Size is
// checked first so differing files usually cost a stat rather than a read.
func sameFileContent(src, dst string) (bool, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return false, err
	}
	// A directory where a file is expected cannot be resolved by copying;
	// report differing and let the caller decide.
	if srcInfo.IsDir() != dstInfo.IsDir() {
		return false, nil
	}
	if srcInfo.Size() != dstInfo.Size() {
		return false, nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer srcFile.Close()
	dstFile, err := os.Open(dst)
	if err != nil {
		return false, err
	}
	defer dstFile.Close()

	// Stream in chunks and stop at the first mismatch rather than loading
	// both files; a bootstrap tree can carry large scripts or skill assets.
	srcBuf := make([]byte, 32*1024)
	dstBuf := make([]byte, 32*1024)
	for {
		sn, srcErr := io.ReadFull(srcFile, srcBuf)
		dn, dstErr := io.ReadFull(dstFile, dstBuf)
		if sn != dn || !bytes.Equal(srcBuf[:sn], dstBuf[:dn]) {
			return false, nil
		}
		srcEOF := errors.Is(srcErr, io.EOF) || errors.Is(srcErr, io.ErrUnexpectedEOF)
		dstEOF := errors.Is(dstErr, io.EOF) || errors.Is(dstErr, io.ErrUnexpectedEOF)
		if srcEOF || dstEOF {
			// Sizes matched going in, so both must end together.
			return srcEOF && dstEOF, nil
		}
		if srcErr != nil {
			return false, srcErr
		}
		if dstErr != nil {
			return false, dstErr
		}
	}
}
