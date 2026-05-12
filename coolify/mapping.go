package coolify

import (
	"fmt"
	"regexp"
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
//   - SlugifyName clamps to 50, not 63, leaving 13 chars of headroom
//     for environment suffixes like `-preview-pr-12` that still need
//     to fit under the 63-byte DNS label limit.
//   - RuntimeForBuildPack defaults to "dockerfile" on unknown — the
//     imported app needs *some* runtime, and Dockerfile is the lowest
//     common denominator.
//   - ParseFirstPort accepts "3000:3000" and takes the left
//     (listening) side — Coolify port specs occasionally arrive in
//     that form.
//   - SlugifyName + AssignKusoSlugs return "x-unnamed" rather than
//     empty when the input is unusable, so the importer doesn't
//     create a service with no name.

// SlugifyName turns an arbitrary Coolify name into a kube-safe slug:
// lowercase, runs of non-[a-z0-9] collapse to "-", trim leading and
// trailing dashes, clamp to 50 chars (leave headroom for
// environment-suffix expansion under the 63-byte DNS label limit).
// Empty input returns "x-unnamed" so callers can't create a slug-less
// kuso resource.
func SlugifyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "x-unnamed"
	}
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
	res := strings.Trim(out.String(), "-")
	if len(res) > 50 {
		res = strings.Trim(res[:50], "-")
	}
	if res == "" {
		res = "x-unnamed"
	}
	return res
}

// AssignKusoSlugs maps Coolify project names to deduplicated kuso
// slugs. Two distinct Coolify projects whose names slugify to the same
// base get a numeric suffix ("acme" → "acme", "acme!" → "acme-2"). Stable
// per call ordering: the first occurrence wins the unsuffixed slug.
func AssignKusoSlugs(coolifyNames []string) map[string]string {
	out := map[string]string{}
	used := map[string]int{}
	for _, name := range coolifyNames {
		base := SlugifyName(name)
		used[base]++
		slug := base
		if used[base] > 1 {
			slug = fmt.Sprintf("%s-%d", base, used[base])
		}
		out[name] = slug
	}
	return out
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
	default:
		return "dockerfile"
	}
}

// ParseFirstPort takes a comma-separated Coolify port list and returns
// the first numeric port, or 0 on no match. Coolify ports may arrive
// as bare integers ("3000") or host:container pairs ("3000:3000"); we
// take the listening (left) side.
func ParseFirstPort(s string) int {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, ":"); i > 0 {
			part = part[:i]
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

// slugifyRE is currently unused by the public API — SlugifyName builds
// its output via a builder rather than regex replace so it can apply
// the "single dash run" + "trim trailing dash on truncation" rules in
// one pass. Kept as a fallback for callers that need the regex shape.
var slugifyRE = regexp.MustCompile(`[^a-z0-9-]+`)

// SlugifyRegex returns the underlying allow-list regex. Exposed for
// callers that want to validate user input directly (e.g. confirm a
// CLI-supplied service name will round-trip through SlugifyName
// unchanged).
func SlugifyRegex() *regexp.Regexp { return slugifyRE }
