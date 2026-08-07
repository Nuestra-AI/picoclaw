package skills

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// fakeRegistry records what reached the inner registry so tests can assert
// that the gate blocked a call rather than merely returning an error.
type fakeRegistry struct {
	name          string
	installCalls  int
	metaCalls     int
	lastVersion   string
	searchResults []SearchResult
	lastLimit     int
}

func (f *fakeRegistry) Name() string { return f.name }

func (f *fakeRegistry) ResolveInstallDirName(target string) (string, error) {
	return strings.ReplaceAll(target, "/", "-"), nil
}

func (f *fakeRegistry) SkillURL(slug, _ string) string { return "https://example.test/" + slug }

func (f *fakeRegistry) Search(_ context.Context, _ string, limit int) ([]SearchResult, error) {
	f.lastLimit = limit
	return f.searchResults, nil
}

func (f *fakeRegistry) GetSkillMeta(_ context.Context, slug string) (*SkillMeta, error) {
	f.metaCalls++
	return &SkillMeta{Slug: slug}, nil
}

func (f *fakeRegistry) DownloadAndInstall(
	_ context.Context,
	_, version, _ string,
) (*InstallResult, error) {
	f.installCalls++
	f.lastVersion = version
	return &InstallResult{Version: version}, nil
}

// newGated mirrors how allowlistProvider.BuildRegistry configures the gate:
// owner/repo parsing is selected by the wrapped registry, not assumed. Tests
// for clawhub pass a registry named "clawhub" and get whole-slug matching.
func newGated(allow []string, inner *fakeRegistry) *allowlistRegistry {
	gated := &allowlistRegistry{inner: inner, allow: parseAllowEntries(allow)}
	if inner.name == "github" {
		gated.githubStyle = true
		gated.webBase = "https://github.com"
	}
	return gated
}

func TestAllowlistBlocksUnlistedInstall(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	_, err := gated.DownloadAndInstall(context.Background(), "attacker/evil", "", t.TempDir())
	if err == nil {
		t.Fatal("expected install of an unlisted repo to be refused")
	}
	if inner.installCalls != 0 {
		t.Fatalf("inner registry was reached despite refusal: %d calls", inner.installCalls)
	}
}

func TestAllowlistPermitsListedInstall(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "Nuestra-AI/skills", "", t.TempDir(),
	); err != nil {
		t.Fatalf("listed repo refused: %v", err)
	}
	if inner.installCalls != 1 {
		t.Fatalf("inner install calls = %d, want 1", inner.installCalls)
	}
}

// An empty allowlist must deny rather than admit: a dropped config key should
// not silently open the registry to everything.
func TestEmptyAllowlistDeniesEverything(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated(nil, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "Nuestra-AI/skills", "", t.TempDir(),
	); err == nil {
		t.Fatal("empty allowlist admitted an install")
	}
	if inner.installCalls != 0 {
		t.Fatalf("inner registry reached with empty allowlist: %d calls", inner.installCalls)
	}
}

// A pinned entry must override a caller-supplied version, or the agent could
// request an arbitrary commit of an allowed repo.
func TestPinnedRefOverridesRequestedVersion(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills@v1.2.3"}, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "Nuestra-AI/skills", "attacker-branch", t.TempDir(),
	); err != nil {
		t.Fatalf("pinned repo refused: %v", err)
	}
	if inner.lastVersion != "v1.2.3" {
		t.Fatalf("version = %q, want the pinned v1.2.3", inner.lastVersion)
	}
}

func TestUnpinnedEntryKeepsRequestedVersion(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "Nuestra-AI/skills", "v9", t.TempDir(),
	); err != nil {
		t.Fatalf("listed repo refused: %v", err)
	}
	if inner.lastVersion != "v9" {
		t.Fatalf("version = %q, want the caller's v9", inner.lastVersion)
	}
}

// Prefix matching must not let a lookalike owner through: "Nuestra-AI-evil"
// shares a textual prefix with "Nuestra-AI" but is a different account.
func TestAllowlistRejectsPrefixLookalikes(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	for _, target := range []string{
		"Nuestra-AI/skills-evil",
		"Nuestra-AI-evil/skills",
		"Nuestra-AI/skillsevil",
	} {
		if _, err := gated.DownloadAndInstall(context.Background(), target, "", t.TempDir()); err == nil {
			t.Errorf("lookalike %q was admitted", target)
		}
	}
	if inner.installCalls != 0 {
		t.Fatalf("inner registry reached by a lookalike: %d calls", inner.installCalls)
	}
}

// A subpath under an allowed repo is permitted; the repo is the vetted unit.
func TestAllowlistPermitsSubpathOfListedRepo(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "Nuestra-AI/skills/pdf-tools", "", t.TempDir(),
	); err != nil {
		t.Fatalf("subpath of a listed repo refused: %v", err)
	}
}

// The URL and shorthand forms of one repo must compare equal, or an operator
// listing the shorthand would be bypassed by someone passing the URL.
func TestAllowlistNormalizesTargetForms(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	for _, target := range []string{
		"Nuestra-AI/skills",
		"NUESTRA-AI/Skills",
		"https://github.com/Nuestra-AI/skills",
		"https://github.com/Nuestra-AI/skills.git",
		"Nuestra-AI/skills@main",
		"/Nuestra-AI/skills/",
	} {
		if _, err := gated.DownloadAndInstall(context.Background(), target, "", t.TempDir()); err != nil {
			t.Errorf("equivalent form %q refused: %v", target, err)
		}
	}
}

func TestSearchDropsDisallowedResults(t *testing.T) {
	inner := &fakeRegistry{
		name: "github",
		searchResults: []SearchResult{
			{Slug: "Nuestra-AI/skills"},
			{Slug: "attacker/evil"},
			{Slug: "Nuestra-AI/skills/pdf"},
		},
	}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	results, err := gated.Search(context.Background(), "pdf", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, result := range results {
		if strings.HasPrefix(result.Slug, "attacker/") {
			t.Errorf("disallowed result %q survived filtering", result.Slug)
		}
	}
}

// Filtering happens after the fetch, so the inner limit must be widened or a
// page of disallowed results would starve the output.
func TestSearchOverfetchesToSurviveFiltering(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.Search(context.Background(), "pdf", 5); err != nil {
		t.Fatalf("search: %v", err)
	}
	if inner.lastLimit <= 5 {
		t.Fatalf("inner limit = %d, want it widened beyond the caller's 5", inner.lastLimit)
	}
}

func TestGetSkillMetaIsGated(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.GetSkillMeta(context.Background(), "attacker/evil"); err == nil {
		t.Fatal("metadata for an unlisted repo was returned")
	}
	if inner.metaCalls != 0 {
		t.Fatalf("inner registry reached for metadata: %d calls", inner.metaCalls)
	}
}

func TestResolveInstallDirNameIsGated(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"Nuestra-AI/skills"}, inner)

	if _, err := gated.ResolveInstallDirName("attacker/evil"); err == nil {
		t.Fatal("install dir resolved for an unlisted repo")
	}
}

func TestParseAllowEntriesDropsBlanks(t *testing.T) {
	entries := parseAllowEntries([]string{"", "   ", "Nuestra-AI/skills", "/"})
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Slug != "Nuestra-AI/skills" {
		t.Fatalf("slug = %q", entries[0].Slug)
	}
}

// The agent path must gate every registry it builds.
func TestAgentRegistryProvidersAreAllowlistGated(t *testing.T) {
	cfg := config.SkillsToolsConfig{}
	cfg.Enabled = true
	cfg.Registries = config.SkillsRegistriesConfig{
		&config.SkillRegistryConfig{Name: "github", Enabled: true, BaseURL: "https://github.com"},
		&config.SkillRegistryConfig{Name: "clawhub", Enabled: true, BaseURL: "https://clawhub.ai"},
	}

	providers := agentRegistryProvidersFromToolsConfig(cfg)
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(providers))
	}
	for _, provider := range providers {
		if _, ok := provider.(allowlistProvider); !ok {
			t.Errorf("agent provider is %T, want the allowlist-gated provider", provider)
		}
	}
}

// Operator-driven callers keep upstream's ungated registries. The gate exists
// to bound tenant input; denying `picoclaw skills install` by default would
// break the operator workflow that seeds skills in the first place.
func TestOperatorRegistryProvidersAreNotGated(t *testing.T) {
	cfg := config.SkillsToolsConfig{}
	cfg.Enabled = true
	cfg.Registries = config.SkillsRegistriesConfig{
		&config.SkillRegistryConfig{Name: "github", Enabled: true, BaseURL: "https://github.com"},
	}

	for _, provider := range registryProvidersFromToolsConfig(cfg) {
		if _, ok := provider.(allowlistProvider); ok {
			t.Error("operator-facing provider was allowlist-gated")
		}
	}
}

// A malformed param block must deny rather than admit.
func TestMalformedAllowParamDeniesEverything(t *testing.T) {
	cfg := config.SkillRegistryConfig{
		Name:    "github",
		Enabled: true,
		// A string where a list belongs: DecodeParam fails and must leave the
		// allowlist empty.
		Param: map[string]any{"allow": "not-a-list"},
	}
	provider, ok := newAllowlistProvider(
		GitHubRegistryConfig{Enabled: true, BaseURL: "https://github.com"}, cfg,
	).(allowlistProvider)
	if !ok {
		t.Fatal("expected an allowlist provider")
	}
	if len(provider.allow) != 0 {
		t.Fatalf("malformed allow param produced %d entries, want 0", len(provider.allow))
	}
}

// The allowlist rules documented in deploy/README.md, asserted against the
// exact entries used as examples there.
func TestDocumentedAllowlistRulesHold(t *testing.T) {
	gated := newGated(
		[]string{"Nuestra-AI/skills@v1.4.0", "some-vendor/pdf-tools"},
		&fakeRegistry{name: "github"},
	)

	allowed := []string{
		"Nuestra-AI/skills",
		"Nuestra-AI/skills/pdf-tools",
		"NUESTRA-AI/Skills.git",
		"https://github.com/Nuestra-AI/skills/",
		"some-vendor/pdf-tools",
	}
	for _, target := range allowed {
		if _, err := gated.ResolveInstallDirName(target); err != nil {
			t.Errorf("documented-allowed %q was refused: %v", target, err)
		}
	}

	blocked := []string{
		"Nuestra-AI-evil/skills",
		"some-vendor/pdf-tools-evil",
		"other/repo",
	}
	for _, target := range blocked {
		if _, err := gated.ResolveInstallDirName(target); err == nil {
			t.Errorf("documented-blocked %q was admitted", target)
		}
	}
}

// Target syntax is per-registry. Clawhub identifiers are opaque single
// segments, so applying GitHub's "@ref" and ".git" stripping to them would
// let an allowlisted "summarize" admit "summarize@anything".
func TestClawhubSlugsAreMatchedWhole(t *testing.T) {
	inner := &fakeRegistry{name: "clawhub"}
	gated := newGated([]string{"summarize"}, inner)

	if _, err := gated.ResolveInstallDirName("summarize"); err != nil {
		t.Fatalf("exact slug refused: %v", err)
	}
	for _, slug := range []string{"summarize@evil", "summarize.git", "summarize@../../etc"} {
		if _, err := gated.ResolveInstallDirName(slug); err == nil {
			t.Errorf("suffixed slug %q was admitted under an exact allowlist entry", slug)
		}
	}
}

// A GitHub Enterprise base URL carries a path prefix. Without stripping it,
// that prefix reads as the owner and an allowlisted repo never matches.
func TestGitHubEnterpriseBasePathIsStripped(t *testing.T) {
	gated := &allowlistRegistry{
		inner:       &fakeRegistry{name: "github"},
		allow:       parseAllowEntries([]string{"owner/repo"}),
		githubStyle: true,
		webBase:     "https://ghe.example.com/git",
	}

	for _, target := range []string{
		"https://ghe.example.com/git/owner/repo",
		"https://ghe.example.com/git/owner/repo/sub",
		"owner/repo",
	} {
		if _, err := gated.ResolveInstallDirName(target); err != nil {
			t.Errorf("allowlisted target %q refused: %v", target, err)
		}
	}
	if _, err := gated.ResolveInstallDirName("https://ghe.example.com/git/other/repo"); err == nil {
		t.Error("a repo outside the allowlist was admitted")
	}
}

// The gated registry must learn its target syntax from the wrapped registry,
// not from the config name it happens to be mounted under.
func TestGitHubStyleIsDerivedFromTheWrappedRegistry(t *testing.T) {
	provider, ok := newAllowlistProvider(
		GitHubRegistryConfig{Enabled: true, BaseURL: "https://github.com"},
		config.SkillRegistryConfig{Name: "some-other-name", Enabled: true},
	).(allowlistProvider)
	if !ok {
		t.Fatal("expected an allowlist provider")
	}
	built, ok := provider.BuildRegistry().(*allowlistRegistry)
	if !ok {
		t.Fatal("expected a gated registry")
	}
	if !built.githubStyle {
		t.Error("github registry mounted under another name lost owner/repo parsing")
	}
}

// A decorator that unwraps to itself must not hang the caller. Registries are
// built during agent construction, so a loop here is a startup hang.
func TestUnwrapRegistryStopsOnCycle(t *testing.T) {
	done := make(chan SkillRegistry, 1)
	go func() {
		self := &selfUnwrappingRegistry{}
		done <- UnwrapRegistry(self)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("UnwrapRegistry did not terminate on a self-referential decorator")
	}
}

type selfUnwrappingRegistry struct{ fakeRegistry }

func (s *selfUnwrappingRegistry) Unwrap() SkillRegistry { return s }

// Entries and targets must go through the same normalization. deploy/README.md
// documents full URLs and ".git" suffixes as valid github entries, so an entry
// compared raw against a normalized target would silently match nothing.
func TestAllowlistEntriesAreNormalizedLikeTargets(t *testing.T) {
	for _, entry := range []string{
		"https://github.com/owner/repo",
		"owner/repo.git",
		"OWNER/REPO",
		"/owner/repo/",
		"owner/repo",
	} {
		gated := newGated([]string{entry}, &fakeRegistry{name: "github"})
		if _, err := gated.ResolveInstallDirName("owner/repo"); err != nil {
			t.Errorf("entry form %q did not match owner/repo: %v", entry, err)
		}
	}
}

// Normalization is case-insensitive end to end. Trimming ".git" before
// lowercasing would leave an uppercase ".GIT" in place and deny the target.
func TestAllowlistMatchingIsCaseInsensitive(t *testing.T) {
	gated := newGated([]string{"owner/repo"}, &fakeRegistry{name: "github"})
	for _, target := range []string{
		"OWNER/REPO.GIT",
		"owner/repo.GIT",
		"HTTPS://GITHUB.COM/OWNER/REPO",
		"Owner/Repo",
	} {
		if _, err := gated.ResolveInstallDirName(target); err != nil {
			t.Errorf("target %q was denied despite an allowlisted repo: %v", target, err)
		}
	}
}

// An Enterprise base path must strip regardless of case.
func TestGitHubEnterpriseBasePathStripIsCaseInsensitive(t *testing.T) {
	gated := &allowlistRegistry{
		inner:       &fakeRegistry{name: "github"},
		allow:       parseAllowEntries([]string{"owner/repo"}),
		githubStyle: true,
		webBase:     "https://GHE.example.com/Git",
	}
	for _, target := range []string{
		"https://ghe.example.com/git/owner/repo",
		"HTTPS://GHE.EXAMPLE.COM/GIT/OWNER/REPO",
	} {
		if _, err := gated.ResolveInstallDirName(target); err != nil {
			t.Errorf("enterprise target %q denied: %v", target, err)
		}
	}
}

// Normalizing entries must not disturb the pinned ref, which is split off
// before the slug is normalized.
func TestEntryNormalizationPreservesPinnedRef(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"https://github.com/owner/repo@v1.2.3"}, inner)

	if _, err := gated.DownloadAndInstall(
		context.Background(), "owner/repo", "attacker-branch", t.TempDir(),
	); err != nil {
		t.Fatalf("pinned URL entry refused: %v", err)
	}
	if inner.lastVersion != "v1.2.3" {
		t.Fatalf("version = %q, want the pinned v1.2.3", inner.lastVersion)
	}
}

// The subpath rule admits anything under an allowed repo, so traversal
// segments must be rejected during normalization rather than left to
// upstream's ValidateInstallTarget downstream.
func TestAllowlistRejectsTraversalSegments(t *testing.T) {
	inner := &fakeRegistry{name: "github"}
	gated := newGated([]string{"owner/repo"}, inner)

	for _, target := range []string{
		"owner/repo/../..",
		"owner/repo/..",
		"owner/repo/../../../etc",
		"owner/repo/./sub",
		"owner/repo//sub",
		"owner\\repo",
	} {
		if _, err := gated.ResolveInstallDirName(target); err == nil {
			t.Errorf("unsafe target %q was admitted", target)
		}
	}
	if inner.installCalls != 0 {
		t.Fatalf("inner registry reached by an unsafe target: %d calls", inner.installCalls)
	}
}

// An empty allowlist cannot match anything, so the query must not be sent to
// an external host at all.
func TestSearchSkipsUpstreamWhenAllowlistIsEmpty(t *testing.T) {
	inner := &fakeRegistry{
		name:          "github",
		searchResults: []SearchResult{{Slug: "owner/repo"}},
	}
	gated := newGated(nil, inner)

	results, err := gated.Search(context.Background(), "pdf", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results from an empty allowlist, want 0", len(results))
	}
	if inner.lastLimit != 0 {
		t.Error("inner registry was queried despite an empty allowlist")
	}
}

// capturingRegistry records the exact strings the gate forwards inward.
type capturingRegistry struct {
	fakeRegistry
	gotSlug string
	gotDir  string
	gotMeta string
}

func (c *capturingRegistry) DownloadAndInstall(
	ctx context.Context,
	slug, version, dir string,
) (*InstallResult, error) {
	c.gotSlug = slug
	return c.fakeRegistry.DownloadAndInstall(ctx, slug, version, dir)
}

func (c *capturingRegistry) ResolveInstallDirName(target string) (string, error) {
	c.gotDir = target
	return c.fakeRegistry.ResolveInstallDirName(target)
}

func (c *capturingRegistry) GetSkillMeta(ctx context.Context, slug string) (*SkillMeta, error) {
	c.gotMeta = slug
	return c.fakeRegistry.GetSkillMeta(ctx, slug)
}

// Matching lowercases both sides, but the lowercased form must never leave
// allowedRef. GitHub stores owner and repo names with canonical case, and the
// install directory is created on a case-sensitive filesystem, so forwarding a
// lowercased slug would fetch or write under the wrong name.
func TestGateForwardsOriginalCaseDownstream(t *testing.T) {
	inner := &capturingRegistry{fakeRegistry: fakeRegistry{name: "github"}}
	gated := &allowlistRegistry{
		// Entry cased differently from the target on purpose.
		inner:       inner,
		allow:       parseAllowEntries([]string{"nuestra-ai/skills"}),
		githubStyle: true,
		webBase:     "https://github.com",
	}

	const target = "Nuestra-AI/skills"
	if _, err := gated.DownloadAndInstall(context.Background(), target, "", t.TempDir()); err != nil {
		t.Fatalf("install refused: %v", err)
	}
	if _, err := gated.ResolveInstallDirName(target); err != nil {
		t.Fatalf("dir name refused: %v", err)
	}
	if _, err := gated.GetSkillMeta(context.Background(), target); err != nil {
		t.Fatalf("metadata refused: %v", err)
	}

	for label, got := range map[string]string{
		"install":  inner.gotSlug,
		"dir name": inner.gotDir,
		"metadata": inner.gotMeta,
	} {
		if got != target {
			t.Errorf("%s received %q, want the original %q", label, got, target)
		}
	}
}

// The subpath rule is github-only. A clawhub identifier is a single opaque
// segment, so "summarize/evil" is a different identifier, not a path under an
// allowlisted "summarize".
func TestClawhubHasNoSubpathRule(t *testing.T) {
	gated := newGated([]string{"summarize"}, &fakeRegistry{name: "clawhub"})
	if _, ok := gated.allowedRef("summarize/evil"); ok {
		t.Error("clawhub admitted a subpath under an allowlisted slug")
	}
	if _, ok := gated.allowedRef("summarize"); !ok {
		t.Error("clawhub refused the exact allowlisted slug")
	}
}

// The Enterprise base path must strip only on a segment boundary; otherwise
// "gitx/owner/repo" normalizes to "x/owner/repo" under base path "git".
func TestEnterpriseBasePathStripsOnSegmentBoundary(t *testing.T) {
	gated := &allowlistRegistry{
		inner:       &fakeRegistry{name: "github"},
		allow:       parseAllowEntries([]string{"owner/repo"}),
		githubStyle: true,
		webBase:     "https://ghe.example.com/git",
	}
	if got := gated.normalizeTarget("https://ghe.example.com/gitx/owner/repo"); got != "gitx/owner/repo" {
		t.Errorf("normalized to %q, want the base path left intact", got)
	}
	if got := gated.normalizeTarget("https://ghe.example.com/git/owner/repo"); got != "owner/repo" {
		t.Errorf("normalized to %q, want owner/repo", got)
	}
}

// Matching ignores an "@ref" suffix, so it must not reach the inner registry:
// upstream parses "owner/repo@v2" as a repo literally named "repo@v2".
func TestRefSuffixIsStrippedBeforeForwarding(t *testing.T) {
	inner := &capturingRegistry{fakeRegistry: fakeRegistry{name: "github"}}
	gated := &allowlistRegistry{
		inner:       inner,
		allow:       parseAllowEntries([]string{"owner/repo"}),
		githubStyle: true,
		webBase:     "https://github.com",
	}

	if _, err := gated.DownloadAndInstall(
		context.Background(), "owner/repo@v2", "", t.TempDir(),
	); err != nil {
		t.Fatalf("install refused: %v", err)
	}
	if inner.gotSlug != "owner/repo" {
		t.Errorf("forwarded slug %q, want owner/repo", inner.gotSlug)
	}
	// The stripped ref becomes the requested version rather than vanishing.
	if inner.lastVersion != "v2" {
		t.Errorf("version %q, want the target's v2", inner.lastVersion)
	}

	if _, err := gated.ResolveInstallDirName("owner/repo@v2"); err != nil {
		t.Fatalf("dir name refused: %v", err)
	}
	if inner.gotDir != "owner/repo" {
		t.Errorf("dir name got %q, want owner/repo", inner.gotDir)
	}

	if _, err := gated.GetSkillMeta(context.Background(), "owner/repo@v2"); err != nil {
		t.Fatalf("metadata refused: %v", err)
	}
	if inner.gotMeta != "owner/repo" {
		t.Errorf("metadata got %q, want owner/repo", inner.gotMeta)
	}
}

// A pinned allowlist entry still overrides a ref carried on the target.
func TestPinnedEntryBeatsRefOnTarget(t *testing.T) {
	inner := &capturingRegistry{fakeRegistry: fakeRegistry{name: "github"}}
	gated := &allowlistRegistry{
		inner:       inner,
		allow:       parseAllowEntries([]string{"owner/repo@v9"}),
		githubStyle: true,
		webBase:     "https://github.com",
	}

	if _, err := gated.DownloadAndInstall(
		context.Background(), "owner/repo@v2", "", t.TempDir(),
	); err != nil {
		t.Fatalf("install refused: %v", err)
	}
	if inner.lastVersion != "v9" {
		t.Errorf("version %q, want the pinned v9", inner.lastVersion)
	}
}

// A URL's host must match the configured registry. Dropping it before
// comparing would normalize "https://evil.test/org/repo" to "org/repo" and
// match an allowlisted repo on a host the operator never approved.
func TestAllowlistRejectsForeignHosts(t *testing.T) {
	gated := newGated([]string{"org/repo"}, &fakeRegistry{name: "github"})

	for _, target := range []string{
		"https://gitlab.example.com/org/repo",
		"https://evil.test/org/repo",
		"https://github.com.evil.test/org/repo",
	} {
		if _, ok := gated.allowedRef(target); ok {
			t.Errorf("target on a foreign host was admitted: %q", target)
		}
	}
	for _, target := range []string{"https://github.com/org/repo", "org/repo"} {
		if _, ok := gated.allowedRef(target); !ok {
			t.Errorf("legitimate target refused: %q", target)
		}
	}
}

// With Enterprise configured, github.com is itself a foreign host.
func TestEnterpriseRegistryRejectsPublicGitHubURLs(t *testing.T) {
	gated := &allowlistRegistry{
		inner:       &fakeRegistry{name: "github"},
		allow:       parseAllowEntries([]string{"owner/repo"}),
		githubStyle: true,
		webBase:     "https://ghe.example.com/git",
	}
	if _, ok := gated.allowedRef("https://ghe.example.com/git/owner/repo"); !ok {
		t.Error("enterprise URL refused")
	}
	if _, ok := gated.allowedRef("https://github.com/owner/repo"); ok {
		t.Error("public github.com admitted by an enterprise-configured registry")
	}
}

// Clawhub identifiers are one opaque segment. A value with a separator can
// never install, so the gate must not report it as allowed.
func TestClawhubRejectsSeparatorsOutright(t *testing.T) {
	gated := newGated([]string{"summarize"}, &fakeRegistry{name: "clawhub"})

	for _, slug := range []string{"summarize/evil", "a\\b", "../etc"} {
		if _, ok := gated.allowedRef(slug); ok {
			t.Errorf("clawhub admitted an uninstallable identifier: %q", slug)
		}
	}
	if _, ok := gated.allowedRef("summarize"); !ok {
		t.Error("clawhub refused a valid allowlisted slug")
	}
}

// Only http(s) URLs, matching upstream's parser. Admitting other schemes
// would report a target as allowed that then fails to install.
func TestAllowlistRejectsNonHTTPSchemes(t *testing.T) {
	gated := newGated([]string{"org/repo"}, &fakeRegistry{name: "github"})

	for _, target := range []string{
		"ssh://github.com/org/repo",
		"git://github.com/org/repo",
		"file://github.com/org/repo",
	} {
		if _, ok := gated.allowedRef(target); ok {
			t.Errorf("unsupported scheme admitted: %q", target)
		}
	}
	for _, target := range []string{
		"https://github.com/org/repo",
		"http://github.com/org/repo",
		"org/repo",
	} {
		if _, ok := gated.allowedRef(target); !ok {
			t.Errorf("supported form refused: %q", target)
		}
	}
}

// A bare "owner" is not a valid install target upstream, so as an allowlist
// entry it is a typo -- and the subpath rule would turn it into a wildcard
// over every repo that owner has.
func TestOwnerOnlyEntryDoesNotWildcardTheOrg(t *testing.T) {
	gated := newGated([]string{"org"}, &fakeRegistry{name: "github"})

	for _, target := range []string{"org/repo", "org/anything-else", "org/x/y"} {
		if _, ok := gated.allowedRef(target); ok {
			t.Errorf("owner-only entry admitted %q", target)
		}
	}
}

// Requiring owner and repo must not break the subpath rule under a proper
// entry, nor clawhub's single-segment identifiers.
func TestOwnerRepoRequirementKeepsSubpathsAndClawhubWorking(t *testing.T) {
	gh := newGated([]string{"org/repo"}, &fakeRegistry{name: "github"})
	for _, target := range []string{"org/repo", "org/repo/pdf-tools", "org/repo/a/b"} {
		if _, ok := gh.allowedRef(target); !ok {
			t.Errorf("subpath of an allowlisted repo refused: %q", target)
		}
	}

	ch := newGated([]string{"summarize"}, &fakeRegistry{name: "clawhub"})
	if _, ok := ch.allowedRef("summarize"); !ok {
		t.Error("clawhub single-segment slug refused")
	}
}

func TestAllowlistErrorNamesTheRegistry(t *testing.T) {
	gated := newGated([]string{"Nuestra-AI/skills"}, &fakeRegistry{name: "clawhub"})
	_, err := gated.DownloadAndInstall(context.Background(), "attacker/evil", "", t.TempDir())
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !strings.Contains(err.Error(), "clawhub") {
		t.Errorf("error %q does not name the registry", err)
	}
	if errors.Unwrap(err) != nil {
		t.Log("note: refusal wraps an inner error")
	}
}
