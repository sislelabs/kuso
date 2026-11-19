package coolify

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Mapping helpers shared by the server-side Coolify importer
// (server-go/internal/http/handlers/import_coolify.go) and the CLI
// (cli/cmd/kusoCli/migrate.go). The two used to carry their own
// copies and had already drifted — slugifyName clamped to 50 in CLI
// but 63 in server, runtimeForBuildPack returned "" for unknown in
// CLI but "dockerfile" in server, parseFirstPort accepted "3000:3000"
// in server but only bare integers in CLI. That class of inconsistency
// produced the bug it was meant to prevent: preview verdicts could
// disagree with apply outcomes.
//
// Canonical choices (with reasons):
//
//   - SlugifyName clamps to 32, the server's service-name limit
//     (serviceNameRE); project slugs get the project limit of 40 and a
//     leading letter (validateProjectName).
//   - RuntimeForBuildPack defaults to "dockerfile" on unknown — the
//     imported app needs *some* runtime, and Dockerfile is the lowest
//     common denominator.
//   - ParseFirstPort accepts "8080:3000" and takes the right
//     (container, i.e. listening) side — Coolify port specs
//     occasionally arrive in host:container form.
//   - SlugifyName + AssignKusoSlugs return "x-unnamed" rather than
//     empty when the input is unusable, so the importer doesn't
//     create a service with no name.

// Server-side name limits: serviceNameRE allows 32 characters,
// validateProjectName 40.
const (
	maxServiceSlug = 32
	maxProjectSlug = 40
)

// SlugifyName turns an arbitrary Coolify name into a kube-safe slug:
// lowercase, runs of non-[a-z0-9] collapse to "-", trim leading and
// trailing dashes, clamp to the 32-character service-name limit.
// Empty input returns "x-unnamed" so callers can't create a slug-less
// kuso resource.
func SlugifyName(s string) string { return slugifyMax(s, maxServiceSlug) }

func slugifyMax(s string, max int) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var out strings.Builder
	prevDash := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				out.WriteRune('-')
				prevDash = true
			}
		}
	}
	res := clampSlug(strings.Trim(out.String(), "-"), max)
	if res == "" {
		res = "x-unnamed"
	}
	return res
}

func clampSlug(s string, max int) string {
	if len(s) > max {
		s = strings.TrimRight(s[:max], "-")
	}
	return s
}

// AssignKusoSlugs maps Coolify project names to deduplicated kuso
// slugs. Two distinct Coolify projects whose names slugify to the same
// base get a numeric suffix ("acme" → "acme", "acme!" → "acme-2"). Stable
// per call ordering: the first occurrence wins the unsuffixed slug.
func AssignKusoSlugs(coolifyNames []string) map[string]string {
	out := map[string]string{}
	taken := map[string]bool{}
	for _, name := range coolifyNames {
		if _, done := out[name]; done {
			continue
		}
		base := slugifyMax(name, maxProjectSlug)
		if base[0] >= '0' && base[0] <= '9' {
			base = clampSlug("x-"+base, maxProjectSlug)
		}
		out[name] = uniqueSlug(base, taken, maxProjectSlug)
	}
	return out
}

// UniqueSlug returns base, or base-2, base-3, … — the first one not in
// taken — and marks it taken. Keyed on names actually handed out, not on
// the base, so a literal "acme-2" can't collide with a suffixed "acme".
// The base is shortened so the result fits the service-name limit.
func UniqueSlug(base string, taken map[string]bool) string {
	return uniqueSlug(base, taken, maxServiceSlug)
}

func uniqueSlug(base string, taken map[string]bool, max int) string {
	slug := base
	for n := 2; taken[slug]; n++ {
		suffix := fmt.Sprintf("-%d", n)
		slug = clampSlug(base, max-len(suffix)) + suffix
	}
	taken[slug] = true
	return slug
}

// ServiceSlugFromApp derives a short, deterministic kuso service name
// from a Coolify Application. Preference order:
//
//  1. Last path segment of GitRepository, with ".git" and any
//     ":branch-uuid" suffix stripped — Coolify sometimes appends both.
//  2. SlugifyName(Application.Name) — the full ugly Coolify name.
//
// SlugifyName guarantees kube-safety + the "x-unnamed" fallback so
// the result is always usable.
func ServiceSlugFromApp(a *Application) string {
	if a == nil {
		return SlugifyName("")
	}
	if a.GitRepository != "" {
		repo := a.GitRepository
		if i := strings.LastIndex(repo, "/"); i >= 0 {
			repo = repo[i+1:]
		}
		if i := strings.Index(repo, ":"); i >= 0 {
			repo = repo[:i]
		}
		repo = strings.TrimSuffix(repo, ".git")
		if s := SlugifyName(repo); s != "" && s != "x-unnamed" {
			return s
		}
	}
	return SlugifyName(a.Name)
}

// NormalizeRepoURL converts a Coolify GitRepository value into a kuso
// repo URL. Coolify stores this field as either an owner/repo slug
// (GitHub App installs, the common case) OR a full http(s):// URL
// (Public Repository mode, non-GitHub hosts). Unconditionally
// prepending "https://github.com/" silently broke kaniko clones for
// the second case — every build would fail with a "couldn't resolve
// https://github.com/https://github.com/..." error.
//
// Trim a trailing ".git" idempotently against both shapes; SSH-style
// remotes ("git@github.com:owner/repo.git") are left alone since kuso
// doesn't build from SSH but a downstream consumer might.
func NormalizeRepoURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "git@") {
		return strings.TrimSuffix(s, ".git")
	}
	return "https://github.com/" + strings.TrimSuffix(s, ".git")
}

// NormalizeBaseDir turns a Coolify base_directory into a kuso
// spec.repo.path. Coolify expresses the repo root as "/" (and often a
// monorepo subdir as "/apps/web"); kuso's repo.path is a RELATIVE path
// under the clone root and rejects a leading slash, so we strip it.
// "/" → "" (root); "/apps/web" → "apps/web"; "" → "". Trailing slashes
// are trimmed so "apps/web/" doesn't drift from "apps/web".
func NormalizeBaseDir(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimSuffix(s, "/")
	return s
}

// RuntimeForBuildPack maps a Coolify BuildPack to a kuso runtime
// string. Unknown values default to "dockerfile" — the safest option
// since the imported app needs *some* build path and a Dockerfile is
// the lowest common denominator.
func RuntimeForBuildPack(bp string) string {
	switch strings.ToLower(strings.TrimSpace(bp)) {
	case "nixpacks":
		return "nixpacks"
	case "static":
		return "static"
	case "dockerimage":
		return "image"
	default:
		return "dockerfile"
	}
}

// ServicePlan is the kuso service an Application maps to. The CLI and
// the server importer both build their create request from it, so the
// two can't drift on which Coolify settings carry over.
type ServicePlan struct {
	Runtime  string
	Port     int
	RepoURL  string
	RepoPath string
	// Branch pins the service's branch, so apps on different branches of
	// one repo don't all build the project default.
	Branch string
	// Dockerfile is relative to RepoPath; "" means "Dockerfile".
	Dockerfile      string
	ImageRepository string
	ImageTag        string
	StaticOutputDir string
	// Dropped has one report line per non-empty Coolify setting that
	// kuso's service create has no field for.
	Dropped []string
}

// PlanService maps an Application onto a kuso service.
func PlanService(a *Application) ServicePlan {
	p := ServicePlan{
		Runtime:  RuntimeForBuildPack(a.BuildPack),
		Port:     ParseFirstPort(a.PortsExposes),
		RepoURL:  NormalizeRepoURL(a.GitRepository),
		RepoPath: NormalizeBaseDir(a.BaseDirectory),
		Branch:   strings.TrimSpace(a.GitBranch),
	}
	drop := func(field, val, why string) {
		if v := strings.TrimSpace(val); v != "" {
			p.Dropped = append(p.Dropped, fmt.Sprintf("%s=%q not carried over — %s", field, v, why))
		}
	}
	switch p.Runtime {
	case "image":
		p.ImageRepository = strings.TrimSpace(a.DockerRegistryImageName)
		p.ImageTag = strings.TrimSpace(a.DockerRegistryImageTag)
		p.RepoURL, p.RepoPath, p.Branch = "", "", ""
	case "dockerfile":
		if df := strings.TrimPrefix(strings.TrimSpace(a.DockerfileLocation), "/"); df != "Dockerfile" {
			p.Dockerfile = df
		}
		drop("dockerfile_target_build", a.DockerfileTarget, "kuso builds the Dockerfile's final stage")
	case "static":
		p.StaticOutputDir = NormalizeBaseDir(a.PublishDirectory)
	}
	drop("install_command", a.InstallCommand, "kuso has no install-command field; put it in the Dockerfile or nixpacks.toml")
	drop("build_command", a.BuildCommand, "kuso has no build-command field; put it in the Dockerfile or nixpacks.toml")
	drop("start_command", a.StartCommand, "set the service command in kuso after import")
	if a.HealthCheckEnabled {
		drop("health_check_path", a.HealthCheckPath, "kuso probes the service port by runtime; configure a health check after import")
		drop("health_check_port", a.HealthCheckPort, "kuso probes the service port by runtime")
	}
	if p.Runtime != "static" {
		drop("publish_directory", a.PublishDirectory, "only static apps serve a publish directory")
	}
	return p
}

// AssignAddonSlugs gives every database in one kuso project its own
// addon name. Coolify scopes names per environment, so a production
// and a staging "db" land in the same kuso project; without the suffix
// the second create 409s and staging's apps get wired to production's
// addon. Ordered by Coolify UUID so a re-run maps each database back
// onto the same addon.
func AssignAddonSlugs(items []Item) map[*Database]string {
	var dbs []*Database
	for _, it := range items {
		if it.Database != nil {
			dbs = append(dbs, it.Database)
		}
	}
	sort.SliceStable(dbs, func(i, j int) bool { return dbs[i].UUID < dbs[j].UUID })
	out := make(map[*Database]string, len(dbs))
	taken := map[string]bool{}
	for _, d := range dbs {
		out[d] = UniqueSlug(SlugifyName(d.Name), taken)
	}
	return out
}

// AddonVersionFromImage derives the kuso addon version from a Coolify
// database image ("postgres:15-alpine" → "15"), "" when the tag is
// missing, "latest" or not numeric (the chart default then applies).
// The chart builds the image from the version, so the precision
// follows how each engine is tagged: mysql needs major.minor (a bare
// "8" is 8.4, not 8.0), clickhouse takes the full tag, the rest the
// major.
func AddonVersionFromImage(kind, image string) string {
	image = strings.TrimSpace(image)
	if at := strings.Index(image, "@"); at >= 0 {
		image = image[:at]
	}
	colon := strings.LastIndex(image, ":")
	if colon < 0 || colon < strings.LastIndex(image, "/") {
		return ""
	}
	tag := image[colon+1:]
	if tag == "" || tag[0] < '0' || tag[0] > '9' {
		return ""
	}
	switch kind {
	case "clickhouse":
		return tag
	case "mysql":
		return leadingVersion(tag, 2)
	default:
		return leadingVersion(tag, 1)
	}
}

// leadingVersion returns the first n dot-separated numeric components
// of tag: leadingVersion("8.0.36-debian", 2) = "8.0".
func leadingVersion(tag string, n int) string {
	end := 0
	for parts := 0; end < len(tag); end++ {
		c := tag[end]
		if c == '.' {
			parts++
			if parts == n {
				break
			}
			continue
		}
		if c < '0' || c > '9' {
			break
		}
	}
	return strings.TrimSuffix(tag[:end], ".")
}

// ParseFirstPort takes a comma-separated Coolify port list and returns
// the first numeric port, or 0 on no match. Coolify ports may arrive
// as bare integers ("3000") or host:container pairs ("8080:3000"); we
// take the container side, which is what the app listens on.
func ParseFirstPort(s string) int {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.LastIndex(part, ":"); i >= 0 {
			part = part[i+1:]
		}
		if n, err := strconv.Atoi(part); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 0
}

// ItemUUID returns the Coolify UUID of an item. Used by the importer
// to match wizard-ticked rows against the server-side re-snapshot
// (the wizard sends UUIDs; the server only trusts its own classifier
// to decide what to provision).
func ItemUUID(it Item) string {
	switch {
	case it.App != nil:
		return it.App.UUID
	case it.Database != nil:
		return it.Database.UUID
	case it.Service != nil:
		return it.Service.UUID
	}
	return ""
}

// ItemKind returns a human-readable resource label used in skip/error
// reasons rendered to the operator.
func ItemKind(it Item) string {
	switch {
	case it.App != nil:
		return "application"
	case it.Database != nil:
		return "database"
	case it.Service != nil:
		return "service"
	}
	return "unknown"
}
