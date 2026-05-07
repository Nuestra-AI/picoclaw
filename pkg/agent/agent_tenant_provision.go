// PicoClaw - Bootstrap-file copy for first-time tenant workspaces.
//
// When MagicForm sends an inbound message with config_dir + workspace
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
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// bootstrapItems lists the files and directories provisionBootstrapFiles
// will copy from configDir into the tenant workspace. Items are looked up
// relative to configDir; missing items are skipped silently because not
// every operator chooses to populate every slot.
var bootstrapItems = []string{
	"AGENT.md",
	"USER.md",
	"SOUL.md",
	"IDENTITY.md",
	"skills",
	"scripts",
}

// provisionBootstrapFiles copies the bootstrapItems from configDir into
// workspace. Files that already exist in workspace are left alone
// (idempotent). Returns the first error encountered; partial copies are
// allowed to remain so the caller can decide whether the tenant agent is
// usable enough to proceed.
//
// Both arguments must be absolute paths already resolved against the
// workspace_root boundary by the caller (extractTenantOverrides handles
// this).
func provisionBootstrapFiles(configDir, workspace string) error {
	if configDir == "" || workspace == "" {
		return errors.New("provisionBootstrapFiles: configDir and workspace must be non-empty")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}
	for _, item := range bootstrapItems {
		src := filepath.Join(configDir, item)
		dst := filepath.Join(workspace, item)

		// Reject anything that would escape the workspace via symlink or
		// crafted item name. filepath.Join cleans `..` but a malicious
		// item value containing platform-specific separators could still
		// produce a surprising path; keep an explicit prefix check.
		if !strings.HasPrefix(dst, workspace) {
			return fmt.Errorf("bootstrap item %q resolves outside workspace", item)
		}

		info, err := os.Stat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("stat %q: %w", src, err)
		}

		if info.IsDir() {
			if err := copyDirIdempotent(src, dst); err != nil {
				return fmt.Errorf("copy dir %q: %w", item, err)
			}
		} else {
			if err := copyFileIfAbsent(src, dst, info.Mode().Perm()); err != nil {
				return fmt.Errorf("copy file %q: %w", item, err)
			}
		}
	}
	return nil
}

// copyDirIdempotent walks src and mirrors files into dst that don't
// already exist. It does not delete files that exist in dst but not src
// (we treat dst as the operator's editable surface).
func copyDirIdempotent(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		// Skip symlinks: we don't want to follow operator-set links that
		// could point outside the workspace, nor recreate them in a place
		// where they'd resolve differently.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return copyFileIfAbsent(path, target, info.Mode().Perm())
	})
}

// copyFileIfAbsent copies src→dst only if dst doesn't already exist.
// Preserves the source mode (within umask). 0o600 is used as a fallback
// floor for sensitive defaults.
func copyFileIfAbsent(src, dst string, mode os.FileMode) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if mode == 0 {
		mode = 0o600
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
