package middleware

import "net/http"

// SecurityHeaders sets baseline browser hardening headers on every response:
//
//   - X-Content-Type-Options: nosniff   — disables MIME sniffing so an HTML
//     response cannot be re-interpreted as something else.
//   - X-Frame-Options: DENY             — refuses to be loaded in any frame,
//     blocking clickjacking against admin endpoints.
//   - Content-Security-Policy           — restricts resource loading to same
//     origin. The ' 'unsafe-inline' allowance for script and style is kept
//     because the bundled web frontend ships inline init code; if that ever
//     changes, tighten the policy here.
//
// The Referrer-Policy header is set separately by ReferrerPolicyNoReferrer.
// HSTS is intentionally not set here because this server is typically
// terminated by a TLS-aware reverse proxy that should own that decision.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set(
			"Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'",
		)
		next.ServeHTTP(w, r)
	})
}
