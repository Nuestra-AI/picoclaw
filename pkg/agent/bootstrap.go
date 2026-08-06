package agent

// CopyBootstrapFiles copies bootstrapItems from srcDir into dstDir, the
// CLI-facing entry point for `picoclaw agent --config-dir`. Missing items are
// skipped.
//
// Existing files in dstDir that differ from the source are preserved and
// returned as workspace-relative paths; set refresh to overwrite them.
// Identical files are never reported.
//
// Copying stops at the first error and the error is returned rather than
// logged, so an interactive caller can surface it.
func CopyBootstrapFiles(srcDir, dstDir string, refresh bool) ([]string, error) {
	return provisionBootstrap(srcDir, dstDir, refresh)
}
