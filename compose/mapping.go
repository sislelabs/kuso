package compose

import (
	"path"
	"strings"
)

// repoSubPath maps a compose build context onto kuso's `path:` field
// (the subdirectory of the repo the build runs in). Only a plain
// relative subdirectory maps; "." (repo root), absolute paths, parent
// escapes, and URL/git contexts return "" — those are still reported
// via the build-context note, just not mapped.
func repoSubPath(context string) string {
	c := strings.TrimSpace(context)
	if c == "" || strings.Contains(c, "://") || strings.HasPrefix(c, "git@") {
		return ""
	}
	c = path.Clean(c)
	if c == "." || strings.HasPrefix(c, "/") || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	return c
}

// Name limits the server enforces: serviceNameRE allows 32 characters
// (addons share the compose slug space, so they get the same cap),
// validateProjectName allows 40 and needs a leading letter.
const (
	maxServiceSlug = 32
	maxProjectSlug = 40
	maxVolumeSlug  = 63
)

// slugify turns an arbitrary compose service / volume name into a
// kube-safe slug of at most max characters: lowercase, runs of
// non-[a-z0-9] collapse to "-", leading and trailing dashes trimmed.
// Empty input returns "x-unnamed" so callers can't emit a slug-less
// resource.
func slugify(s string, max int) string {
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
	res := strings.Trim(out.String(), "-")
	if res == "" {
		return "x-unnamed"
	}
	return clampSlug(res, max)
}

func clampSlug(s string, max int) string {
	if len(s) > max {
		s = strings.TrimRight(s[:max], "-")
	}
	return s
}

// projectSlug is slugify with the project rules: 40 characters and a
// leading letter.
func projectSlug(s string) string {
	res := slugify(s, maxProjectSlug)
	if res[0] >= '0' && res[0] <= '9' {
		res = clampSlug("x-"+res, maxProjectSlug)
	}
	return res
}

// dedupeSlugs assigns a stable, collision-free service slug per input
// name, preserving order. The first name keeps its slug; a later name
// that slugifies to the same string becomes "<slug>-2", "<slug>-3", …
// — the same scheme the coolify importer uses so report and apply
// agree. The suffix skips names already handed out, so a literal
// "worker-2" can't collide with a suffixed "worker".
func dedupeSlugs(names []string) map[string]string {
	out := map[string]string{}
	taken := map[string]bool{}
	for _, n := range names {
		if _, done := out[n]; done {
			continue
		}
		out[n] = uniqueSlug(slugify(n, maxServiceSlug), taken, maxServiceSlug)
	}
	return out
}

// uniqueSlug returns base, or base-2, base-3, … — the first not in
// taken — and marks it taken. The base is shortened as needed so the
// suffixed slug still fits in max.
func uniqueSlug(base string, taken map[string]bool, max int) string {
	slug := base
	for i := 2; taken[slug]; i++ {
		suffix := "-" + itoa(i)
		slug = clampSlug(base, max-len(suffix)) + suffix
	}
	taken[slug] = true
	return slug
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
