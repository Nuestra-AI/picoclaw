// Helpers for sending HTTP error responses without leaking internal error
// detail. The standard pattern in this package was http.Error(w, fmt.Sprintf(
// "Failed to X: %v", err), 500), which echoes the raw error back to the
// client — leaking file paths, library internals, schema hints, etc.
//
// The helpers below preserve the operator-visible error (server-side log)
// while sending a generic message to the client. Use these for any error
// that originated in internal code (file I/O, config parsing, DB ops,
// gateway IPC). It is fine to keep echoing user-validation errors directly
// — those are by definition already safe to show.

package api

import (
	"fmt"
	"net/http"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// writeSafeError logs the underlying error with the given context message
// and sends the client an HTTP error response containing only the generic
// message and status code. Use for errors from internal code that may
// carry sensitive detail (paths, schemas, library internals).
//
// Example:
//
//	if err := config.SaveConfig(path, &cfg); err != nil {
//	    writeSafeError(w, http.StatusInternalServerError, "Failed to save config", err)
//	    return
//	}
func writeSafeError(w http.ResponseWriter, status int, generic string, err error) {
	logger.ErrorCF("api", generic, map[string]any{"error": err.Error()})
	http.Error(w, generic, status)
}

// safeErrorf is the same idea for cases where the generic message itself
// includes user-supplied identifiers that are safe to echo (e.g. a slug
// the client just sent). Internal err is logged, only the formatted
// generic is returned.
func safeErrorf(w http.ResponseWriter, status int, err error, format string, args ...any) {
	generic := fmt.Sprintf(format, args...)
	logger.ErrorCF("api", generic, map[string]any{"error": err.Error()})
	http.Error(w, generic, status)
}
