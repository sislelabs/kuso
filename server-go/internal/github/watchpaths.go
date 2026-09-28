package github

import (
	"encoding/json"
	"path"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// Monorepo push filtering: a push builds a service only when a changed
// file matches its watch paths, and a HEAD commit carrying a skip
// directive builds nothing.
//
// The rule that governs every branch here: never skip on uncertainty.
// If the push payload can't tell us the full set of changed files, the
// service builds. A redundant build costs minutes; a skipped one ships
// stale code silently.
//
// Scope: GitHub push events only. PR previews ignore both watch paths
// and skip directives — the pull_request payload carries neither the
// changed files nor the head commit message, and a stale preview is
// more confusing than a redundant one.

// GitHub truncates push payloads (commit list and per-push file list).
// Hitting either cap means the list we see may be partial, so the push
// is treated as "files unknown". Treating a merely-large push as
// truncated only costs a build.
const (
	pushCommitCap = 20
	pushFileCap   = 3000
)

var skipCIDirectives = []string{"[skip ci]", "[ci skip]", "[skip kuso]"}

type pushChanges struct {
	// Known is true only when Files is believed to be the complete set of
	// paths the push changed.
	Known  bool
	Files  []string
	Reason string // why Known is false
}

// parsePushChanges extracts the changed-file set from a push webhook
// body. It decodes separately from pushEvent so the filter owns its own
// wire shape.
func parsePushChanges(body []byte) pushChanges {
	var p struct {
		Created bool `json:"created"`
		Forced  bool `json:"forced"`
		Commits []struct {
			Added    []string `json:"added"`
			Modified []string `json:"modified"`
			Removed  []string `json:"removed"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return pushChanges{Reason: "push payload commits undecodable"}
	}
	switch {
	case p.Created:
		// A new branch's commit list is relative to an unknown base.
		return pushChanges{Reason: "branch created"}
	case p.Forced:
		return pushChanges{Reason: "force push"}
	case len(p.Commits) == 0:
		return pushChanges{Reason: "no commits in payload"}
	case len(p.Commits) >= pushCommitCap:
		return pushChanges{Reason: "commit list may be truncated"}
	}
	seen := map[string]bool{}
	total := 0
	for _, c := range p.Commits {
		for _, list := range [][]string{c.Added, c.Modified, c.Removed} {
			total += len(list)
			for _, f := range list {
				seen[f] = true
			}
		}
	}
	if total >= pushFileCap {
		return pushChanges{Reason: "file list may be truncated"}
	}
	if len(seen) == 0 {
		// Empty commits are a common "redeploy please" gesture.
		return pushChanges{Reason: "push lists no changed files"}
	}
	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	sort.Strings(files)
	return pushChanges{Known: true, Files: files}
}

// skipCIDirective reports whether a commit message asks kuso to skip
// the push, returning the matched directive.
func skipCIDirective(message string) (string, bool) {
	lower := strings.ToLower(message)
	for _, d := range skipCIDirectives {
		if strings.Contains(lower, d) {
			return d, true
		}
	}
	return "", false
}

// effectiveWatchPaths returns the patterns gating push builds for a
// service. nil means "every push". Filtering is opt-in: a monorepo
// service without watch paths still rebuilds on every push, because a
// default of <path>/** would silently stop rebuilds when shared code
// outside the service's directory (packages/, the lockfile) changes.
func effectiveWatchPaths(spec *kube.KusoServiceSpec) []string {
	var out []string
	for _, p := range spec.WatchPaths {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// serviceWatchPaths decodes a listed KusoService and returns its
// effective watch paths. A service that fails to decode watches
// everything (build rather than skip).
func serviceWatchPaths(u *unstructured.Unstructured) []string {
	var svc kube.KusoService
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &svc); err != nil {
		return nil
	}
	return effectiveWatchPaths(&svc.Spec)
}

// pushTouchesService decides whether a push should build a service with
// the given watch patterns. A false result always carries a reason.
func pushTouchesService(patterns []string, ch pushChanges) (bool, string) {
	if len(patterns) == 0 || !ch.Known {
		return true, ""
	}
	for _, f := range ch.Files {
		for _, p := range patterns {
			if matchWatchGlob(p, f) {
				return true, ""
			}
		}
	}
	return false, "no changed file matches watch paths " + strings.Join(patterns, ",")
}

func normalizeWatchPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return strings.TrimSuffix(p, "/")
}

// matchWatchGlob matches a repo-relative file path against a pattern
// where `**` spans any number of path segments (including zero) and
// other segments use path.Match syntax. A pattern with no glob
// characters also matches everything beneath it as a directory.
func matchWatchGlob(pattern, file string) bool {
	pattern = normalizeWatchPath(pattern)
	if pattern == "" {
		return false
	}
	if !strings.ContainsAny(pattern, "*?[") && strings.HasPrefix(file, pattern+"/") {
		return true
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if matchSegments(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
