// PicoClaw - Allowlist gate over the upstream skill registries.
//
// A skill is markdown injected into the agent prompt plus a scripts/ tree
// the exec tool can run, so installing one is closer to running third-party
// code than to fetching data. Upstream accepts any clawhub slug or any
// public GitHub repo, which makes the install target attacker-controlled
// whenever the agent acts on tenant input.
//
// This wraps the upstream registries rather than replacing them: search and
// install still go through the original implementation (and keep its fixes),
// but every target is checked against an operator-configured allowlist
// first, and search results outside the list are dropped.
//
// Applied by the agent-scoped constructor in nuestra_allowlist_manager.go,
// not by overriding upstream's builders. Operator-driven callers (the CLI,
// the launcher's admin API) keep upstream's ungated registries; see the "do
// not make the gate global" note in NUESTRA_CUSTOMIZATIONS.md entry 12.

package skills

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// allowlistPrivateConfig is read from the registry's "param" block.
//
// Entries are matched case-insensitively. For github they are "owner/repo",
// optionally pinned as "owner/repo@ref"; for clawhub they are bare slugs.
type allowlistPrivateConfig struct {
	Allow []string `json:"allow"`
}

// allowlistRegistry decorates a SkillRegistry with an install allowlist.
//
// An empty allowlist denies everything. This is deliberate: the failure mode
// of an empty-means-all reading is a registry that silently allows the whole
// internet the moment a config key is dropped, which is the condition this
// type exists to prevent.
type allowlistRegistry struct {
	inner SkillRegistry
	allow []allowEntry
	// githubStyle selects owner/repo target parsing. Set from the wrapped
	// registry's concrete type rather than its name, so a GitHub registry
	// mounted under a different config name still normalizes correctly.
	githubStyle bool
	// webBase is the wrapped registry's base URL, used to strip an
	// Enterprise base path before matching.
	webBase string
}

// allowEntry is one parsed allowlist line. Ref is empty when the entry does
// not pin a version.
type allowEntry struct {
	Slug string
	Ref  string
}

// parseAllowEntries splits configured entries into slug and pinned ref. Blank
// entries are dropped so a stray comma in JSON cannot widen the list.
//
// The slug is kept as written: normalizing it needs the registry's target
// syntax, which is not known until the gate is built. allowedRef applies the
// same normalization to entries and targets so the two always agree.
func parseAllowEntries(raw []string) []allowEntry {
	entries := make([]allowEntry, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		slug, ref := item, ""
		if at := strings.LastIndex(item, "@"); at > 0 {
			slug, ref = item[:at], item[at+1:]
		}
		if strings.Trim(strings.TrimSpace(slug), "/") == "" {
			continue
		}
		entries = append(entries, allowEntry{
			Slug: slug,
			Ref:  strings.TrimSpace(ref),
		})
	}
	return entries
}

// allowedRef reports whether target is allowed, and returns the ref the entry
// pins it to ("" when unpinned).
//
// Matching is on the leading owner/repo (github) or the whole slug (clawhub);
// a deeper subpath under an allowed repo is permitted for github, since the
// repo is the unit an operator vets. Path traversal is not a concern here
// because the value is only compared, never joined onto a filesystem path.
func (r *allowlistRegistry) allowedRef(target string) (string, bool) {
	normalized := r.normalizeTarget(target)
	if normalized == "" {
		return "", false
	}
	for _, entry := range r.allow {
		// Entries go through the same normalization as targets. Comparing a
		// raw entry against a normalized target would silently reject the
		// documented URL and ".git" entry forms.
		slug := r.normalizeTarget(entry.Slug)
		if slug == "" {
			continue
		}
		if normalized == slug {
			return entry.Ref, true
		}
		// The subpath rule is github-only: a clawhub identifier is a single
		// opaque segment, so "summarize/evil" is not a path under an
		// allowlisted "summarize" -- it is a different identifier.
		if r.githubStyle && strings.HasPrefix(normalized, slug+"/") {
			return entry.Ref, true
		}
	}
	return "", false
}

// forwardTarget is the value passed to the inner registry.
//
// Upstream parses "owner/repo@v1" as a repo literally named "repo@v1", so a
// target the gate admitted by stripping the suffix would fetch a repo that
// does not exist. The suffix is removed here and returned separately so the
// caller can use it as the requested version.
//
// Case is preserved: GitHub stores owner and repo names cased, and the
// install directory is created on a case-sensitive filesystem.
func (r *allowlistRegistry) forwardTarget(target string) (cleaned, ref string) {
	if !r.githubStyle {
		return target, ""
	}
	trimmed := strings.TrimSpace(target)
	if at := strings.LastIndex(trimmed, "@"); at > 0 {
		return trimmed[:at], trimmed[at+1:]
	}
	return trimmed, ""
}

// normalizeTarget reduces an install target to the lowercase form compared
// against the allowlist.
//
// Target syntax is per-registry, so normalization has to be too. Applying
// GitHub's shorthand rules everywhere is unsafe: clawhub identifiers are
// opaque single segments, and stripping an "@ref" suffix from one would let
// an allowlisted "summarize" admit "summarize@anything".
func (r *allowlistRegistry) normalizeTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	var normalized string
	if r.githubStyle {
		normalized = normalizeGitHubAllowTarget(target, r.webBase)
	} else {
		// A clawhub identifier is one opaque segment. Anything with a
		// separator can never install (ValidateSkillIdentifier rejects it),
		// so admitting it here would report "allowed" for a target that
		// fails downstream.
		if strings.ContainsAny(target, "/\\") {
			return ""
		}
		normalized = strings.ToLower(target)
	}
	if hasUnsafeSegment(normalized) {
		return ""
	}
	return normalized
}

// hasUnsafeSegment reports whether any path segment is empty, ".", or "..".
//
// The subpath rule admits anything under an allowed repo, so without this
// "owner/repo/../.." matches an allowlisted "owner/repo". Upstream's
// ValidateInstallTarget rejects these before a filesystem path is built, but
// the gate should not approve a target it has no business approving --
// depending on a downstream check to contain what this one waved through is
// how a later refactor turns a denial into an escape.
func hasUnsafeSegment(target string) bool {
	if target == "" {
		return false
	}
	if strings.Contains(target, "\\") {
		return true
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

// normalizeGitHubAllowTarget reduces a GitHub target to "owner/repo[/subpath]"
// so the URL and shorthand forms of one repo compare equal.
//
// webBase is the registry's configured base URL. It is used to strip an
// Enterprise base path ("https://ghe.example.com/git/owner/repo"), where the
// path prefix would otherwise be read as the owner and never match.
func normalizeGitHubAllowTarget(target, webBase string) string {
	// Lowercase up front so every comparison below is case-insensitive: the
	// base-path prefix and the ".git" suffix would otherwise survive in
	// uppercase and deny a target the operator allowlisted.
	target = strings.ToLower(strings.TrimSpace(target))
	if at := strings.LastIndex(target, "@"); at > 0 {
		target = target[:at]
	}
	if idx := strings.Index(target, "://"); idx >= 0 {
		// Only http(s), matching upstream's parser. Admitting "ssh://" or
		// "git://" here would report a target as allowed that then fails to
		// install.
		if scheme := target[:idx]; scheme != "http" && scheme != "https" {
			return ""
		}
		rest := target[idx+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return ""
		}
		// Only URLs on the configured host are comparable. Dropping the host
		// unconditionally would normalize "https://evil.test/org/repo" to
		// "org/repo" and match an allowlisted repo on a host the operator
		// never approved.
		if !gitHubHostMatches(rest[:slash], webBase) {
			return ""
		}
		target = rest[slash+1:]
		// Drop the base URL's own path segments ("/git" above) so what
		// remains starts at the owner. The prefix must end on a segment
		// boundary: a bare TrimPrefix would turn "gitx/owner/repo" into
		// "x/owner/repo" and compare a mangled owner.
		if prefix := gitHubBasePathPrefix(webBase); prefix != "" {
			rest := strings.TrimLeft(target, "/")
			switch {
			case rest == prefix:
				target = ""
			case strings.HasPrefix(rest, prefix+"/"):
				target = rest[len(prefix)+1:]
			}
		}
	}
	target = strings.Trim(target, "/")
	target = strings.TrimSuffix(target, ".git")
	// Require owner and repo. A bare "owner" is not a valid install target
	// upstream, so as an allowlist entry it can only be a typo -- and the
	// subpath rule would turn it into a wildcard over every repo the owner
	// has. Rejecting it keeps a malformed entry from widening the list.
	if strings.Count(target, "/") < 1 {
		return ""
	}
	return target
}

// gitHubBasePathPrefix returns the lowercase path portion of a base URL
// ("https://ghe.example.com/git" -> "git"), or "" when it has none.
func gitHubBasePathPrefix(webBase string) string {
	webBase = strings.TrimSpace(webBase)
	if webBase == "" {
		return ""
	}
	idx := strings.Index(webBase, "://")
	if idx < 0 {
		return ""
	}
	rest := webBase[idx+3:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return ""
	}
	return strings.ToLower(strings.Trim(rest[slash+1:], "/"))
}

// gitHubHostMatches reports whether host is the one the registry is
// configured for. An empty webBase falls back to github.com, matching the
// default the registry itself uses.
//
// The port is compared as written: a target naming an explicit port the base
// URL does not is a different endpoint, and treating it as equal would let a
// target reach a host the operator did not configure.
func gitHubHostMatches(host, webBase string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	want := "github.com"
	if base := strings.TrimSpace(webBase); base != "" {
		if idx := strings.Index(base, "://"); idx >= 0 {
			rest := base[idx+3:]
			if slash := strings.Index(rest, "/"); slash >= 0 {
				rest = rest[:slash]
			}
			if rest != "" {
				want = strings.ToLower(rest)
			}
		}
	}
	return host == want
}

func (r *allowlistRegistry) Name() string { return r.inner.Name() }

// Unwrap exposes the wrapped registry.
//
// Callers type-assert to the concrete registry types to read fields the
// interface does not carry (config_bridge.go asserts *GitHubRegistry to reach
// its base URL). Wrapping otherwise makes those assertions fail silently and
// degrade behavior, so the decorator stays transparent to them via
// UnwrapRegistry.
func (r *allowlistRegistry) Unwrap() SkillRegistry { return r.inner }

// registryUnwrapper is implemented by decorators around a SkillRegistry.
type registryUnwrapper interface {
	Unwrap() SkillRegistry
}

// UnwrapRegistry returns the underlying registry, following any decorators.
// Use it before type-asserting to a concrete registry type.
//
// A decorator whose Unwrap returns itself, or a cycle between two of them,
// would otherwise hang every caller. Registries are built during agent
// construction, so that would be a startup hang rather than a failed request.
func UnwrapRegistry(registry SkillRegistry) SkillRegistry {
	seen := map[SkillRegistry]struct{}{}
	for {
		unwrapper, ok := registry.(registryUnwrapper)
		if !ok {
			return registry
		}
		if _, cycled := seen[registry]; cycled {
			return registry
		}
		seen[registry] = struct{}{}
		inner := unwrapper.Unwrap()
		if inner == nil {
			return registry
		}
		registry = inner
	}
}

func (r *allowlistRegistry) ResolveInstallDirName(target string) (string, error) {
	if _, ok := r.allowedRef(target); !ok {
		return "", fmt.Errorf("skill %q is not in the %s allowlist", target, r.inner.Name())
	}
	cleaned, _ := r.forwardTarget(target)
	return r.inner.ResolveInstallDirName(cleaned)
}

// SkillURL is not gated: it formats a URL and performs no fetch. Returning
// empty for disallowed targets would only degrade error messages.
func (r *allowlistRegistry) SkillURL(slug, version string) string {
	return r.inner.SkillURL(slug, version)
}

// Search filters results to the allowlist. Suggesting a skill that install
// will refuse wastes a turn and invites the agent to retry, so the listing
// is narrowed to what can actually be installed.
//
// The limit is applied after filtering, and the inner search is widened so a
// page of disallowed results cannot starve the output.
func (r *allowlistRegistry) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	// Nothing can survive filtering, so skip the request entirely rather than
	// send a tenant-supplied query to an external host for a result set that
	// is discarded.
	if len(r.allow) == 0 {
		return nil, nil
	}
	innerLimit := limit
	if innerLimit > 0 {
		innerLimit *= searchOverfetchFactor
	}
	results, err := r.inner.Search(ctx, query, innerLimit)
	if err != nil {
		return nil, err
	}
	filtered := make([]SearchResult, 0, len(results))
	for _, result := range results {
		if _, ok := r.allowedRef(result.Slug); !ok {
			continue
		}
		filtered = append(filtered, result)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

// searchOverfetchFactor widens the inner search so filtering still has
// candidates to return. Kept small: each multiple is upstream request cost.
const searchOverfetchFactor = 4

func (r *allowlistRegistry) GetSkillMeta(ctx context.Context, slug string) (*SkillMeta, error) {
	if _, ok := r.allowedRef(slug); !ok {
		return nil, fmt.Errorf("skill %q is not in the %s allowlist", slug, r.inner.Name())
	}
	cleaned, _ := r.forwardTarget(slug)
	return r.inner.GetSkillMeta(ctx, cleaned)
}

// DownloadAndInstall is the gate that matters: everything else only reads
// metadata, while this writes executable content into a tenant workspace.
//
// When the matched entry pins a ref, that ref replaces the caller's version.
// Honoring a caller-supplied version against a pinned entry would let the
// agent request an arbitrary commit of an allowed repo, which defeats the
// point of pinning.
func (r *allowlistRegistry) DownloadAndInstall(
	ctx context.Context,
	slug, version, targetDir string,
) (*InstallResult, error) {
	pinnedRef, ok := r.allowedRef(slug)
	if !ok {
		return nil, fmt.Errorf("skill %q is not in the %s allowlist", slug, r.inner.Name())
	}
	cleaned, targetRef := r.forwardTarget(slug)
	// A ref in the target acts as the requested version when none was given
	// explicitly; matching already ignored it, so dropping it silently would
	// install a different ref than asked for.
	if version == "" {
		version = targetRef
	}
	if pinnedRef != "" {
		version = pinnedRef
	}
	return r.inner.DownloadAndInstall(ctx, cleaned, version, targetDir)
}

// NormalizeInstallTarget forwards to the inner registry when it supports it,
// preserving the origin-metadata behavior callers rely on.
func (r *allowlistRegistry) NormalizeInstallTarget(target string) string {
	return NormalizeInstallTargetForRegistryInstance(r.inner, target)
}

// allowlistProvider wraps an upstream provider so the allowlist is applied
// at build time, keeping the enabled/disabled semantics of the inner config.
type allowlistProvider struct {
	inner RegistryProvider
	allow []allowEntry
}

func (p allowlistProvider) IsEnabled() bool {
	return p.inner != nil && p.inner.IsEnabled()
}

func (p allowlistProvider) BuildRegistry() SkillRegistry {
	if p.inner == nil {
		return nil
	}
	inner := p.inner.BuildRegistry()
	if inner == nil {
		return nil
	}
	gated := &allowlistRegistry{inner: inner, allow: p.allow}
	if gh, ok := UnwrapRegistry(inner).(*GitHubRegistry); ok {
		gated.githubStyle = true
		gated.webBase = gh.webBase
	}
	return gated
}

// newAllowlistProvider builds a provider that gates inner with the allowlist
// from cfg.
func newAllowlistProvider(inner RegistryProvider, cfg config.SkillRegistryConfig) RegistryProvider {
	privateCfg := allowlistPrivateConfig{}
	// A malformed param block yields no entries, which denies rather than
	// admits; the decode error is not worth failing startup over.
	_ = cfg.DecodeParam(&privateCfg)
	return allowlistProvider{
		inner: inner,
		allow: parseAllowEntries(privateCfg.Allow),
	}
}
