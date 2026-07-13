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

// slugify turns an arbitrary compose service / volume name into a
// kube-safe slug: lowercase, runs of non-[a-z0-9] collapse to "-",
// trim leading and trailing dashes, clamp to 50 chars (leaving
// headroom for environment-suffix expansion under the 63-byte DNS
// label limit — the same clamp the coolify importer settled on).
// Empty input returns "x-unnamed" so callers can't emit a slug-less
// resource.
func slugify(s string) string {
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
	if res == "" {
		return "x-unnamed"
	}
	if len(res) > 50 {
		res = strings.Trim(res[:50], "-")
	}
	return res
}

// dedupeSlugs assigns a stable, collision-free slug per input name,
// preserving order. The first name keeps its slug; a later name that
// slugifies to the same string becomes "<slug>-2", "<slug>-3", … —
// the same scheme the coolify importer uses so report and apply agree.
func dedupeSlugs(names []string) map[string]string {
	out := map[string]string{}
	seen := map[string]int{}
	for _, n := range names {
		base := slugify(n)
		seen[base]++
		if seen[base] == 1 {
			out[n] = base
			continue
		}
		out[n] = base + "-" + itoa(seen[base])
	}
	return out
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
