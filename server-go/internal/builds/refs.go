// Pure helpers extracted from builds.go for readability. These have
// no receiver types and no state — they're string / struct shaping
// that doesn't really belong on the 2800-line builds.go entry-point
// file. Co-located here so a future contributor adding a new ref-
// shaping function (e.g. a different image-tag scheme) lands one
// place to look.
package builds

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// ImageTag returns the immutable tag we push for a build. For a full
// 40-char SHA, the leading 12 chars (matches `git rev-parse --short`).
// For an arbitrary ref string (a branch name in the dev path), the
// slugified form.
//
// The slugify is NOT optional: a Docker tag may only contain
// [A-Za-z0-9_.-], so a branch like `deploy/kuso` or `tenant/acme`
// produced an invalid tag and the image push failed. The old comment
// here claimed branch names were "already kube-name-safe via shortRef"
// — true of the CR name and Job labels, which do route through
// shortRef, but this function did not, so the slash survived into the
// tag. Routing through shortRef makes every ref→identifier path use
// one slugifier. Callers that need the raw ref must keep it separately
// (spec.Branch already does).
func ImageTag(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	return shortRef(ref)
}

// buildCRName composes the canonical KusoBuild CR name. The format
// `<project>-<service>-<short-ref>` keeps the name unique per
// (service, ref) tuple so repeated builds of the same commit collapse
// to one CR (idempotent retry without spawning duplicates).
func buildCRName(project, service, ref string) string {
	return clampLabelValue(fmt.Sprintf("%s-%s-%s", project, service, shortRef(ref)))
}

// maxLabelValue is kube's hard ceiling on a label value and on the name
// segment of most objects. Exceeding it is a 422 from the apiserver, not
// a truncation.
const maxLabelValue = 63

// clampLabelValue truncates to kube's 63-byte limit, appending an 8-hex
// digest of the original so two long values that share a prefix don't
// collide after truncation.
//
// validateProjectName allows 40 chars and serviceNameRE allows 32, so a
// service FQN can reach 73 — past the ceiling. The build name and the
// kuso.sislelabs.com/service label were both stamped raw, so the
// apiserver rejected CreateKusoBuild with a 422 that surfaced as an
// opaque 500 and the build never ran. Same incident class as the
// slash-in-branch brick, on the service axis rather than the ref axis.
func clampLabelValue(v string) string {
	if len(v) <= maxLabelValue {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	suffix := "-" + hex.EncodeToString(sum[:])[:8]
	return v[:maxLabelValue-len(suffix)] + suffix
}

// shortRef trims a 40-char SHA to its 12-char short form, or
// slugifies an arbitrary ref string to a kube-name-safe slice
// (lowercase letters/digits/dashes, ≤32 chars).
//
// Capping the ref alone does NOT keep the build name under 63 — project
// (≤40) + service (≤32) already exceeds it before the ref is appended.
// buildCRName runs the result through clampLabelValue for that.
func shortRef(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	const max = 32
	out := make([]byte, 0, len(ref))
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == '-':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return strings.Trim(string(out), "-")
}

// buildCacheDisabled reads the per-project escape hatch annotation.
// Set kuso.sislelabs.com/build-cache-disabled=true on a KusoProject
// to skip the persistent build cache for every service in that
// project. Useful when a corrupted PVC is causing build failures —
// users can flip the annotation, the next build runs cold, and they
// can delete the broken PVC by hand.
func buildCacheDisabled(proj *kube.KusoProject) bool {
	if proj == nil || proj.Annotations == nil {
		return false
	}
	return proj.Annotations["kuso.sislelabs.com/build-cache-disabled"] == "true"
}

// githubInstallationID resolves the GitHub App installation ID to use
// for cloning a service's repo. Service-level wins over project-level
// so a project linked to org A can host a service whose repo lives
// in org B (different installations). Falls back to project-level
// when the service didn't override (the common case), then 0 for
// fully public repos.
//
// Pre-fix this only checked project-level, so a service whose repo
// was in a different org than the project's defaultRepo cloned
// unauthenticated and hit `fatal: could not read Username for
// 'https://github.com'`.
//
// As of v0.9.54, even when both spec slots are 0 the build path
// auto-resolves via the GH-app cache before reaching this fallback,
// so a project pointed at a private repo whose org has the App
// installed Just Works without manual installationID plumbing.
func githubInstallationID(proj *kube.KusoProject, svc *kube.KusoService) int64 {
	if svc != nil && svc.Spec.Github != nil && svc.Spec.Github.InstallationID > 0 {
		return svc.Spec.Github.InstallationID
	}
	if proj == nil || proj.Spec.GitHub == nil {
		return 0
	}
	return proj.Spec.GitHub.InstallationID
}

// splitGithubURL parses owner/repo from the canonical github URL
// shapes the user types into AddService. Returns ("", "") for
// non-github URLs. Trims a trailing ".git". Lightweight string ops;
// the canonical implementation lives in the github package
// (ParseGithubRepoURL) — duplicated here to avoid an import cycle
// (github already depends on db, builds depends on neither).
func splitGithubURL(raw string) (owner, repo string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", ""
	}
	if strings.HasPrefix(s, "git@github.com:") {
		s = strings.TrimPrefix(s, "git@github.com:")
	} else {
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		s = strings.TrimPrefix(s, "git+")
		if !strings.HasPrefix(s, "github.com/") && !strings.HasPrefix(s, "www.github.com/") {
			return "", ""
		}
		s = strings.TrimPrefix(s, "www.")
		s = strings.TrimPrefix(s, "github.com/")
	}
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// completedCondition picks the terminal Job condition (Complete or
// Failed) out of the kaniko Job's status, or nil if neither has been
// stamped yet. The Status=="True" check is load-bearing — kube
// occasionally stamps a condition with Status="False" mid-transition
// that would otherwise look terminal.
func completedCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		c := &job.Status.Conditions[i]
		if c.Status != "True" {
			continue
		}
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c
		}
	}
	return nil
}

// containerNames returns each container's Name. Used by the log
// archiver to enumerate which streams to read.
func containerNames(cs []corev1.Container) []string {
	out := make([]string, 0, len(cs))
	for i := range cs {
		out = append(out, cs[i].Name)
	}
	return out
}

// --- Boundary validation for values that reach the clone shell -------
//
// repoURL and branch/ref are interpolated into the clone init
// container's /bin/sh script (buildcontroller/render.go). shellQuote
// single-quotes them there, which is correct — but its doc comment
// asserts that the kuso-server boundary already validated these
// fields, and for a long time nothing did. These functions make that
// claim true. Belt AND braces: quoting protects the shell, validation
// keeps hostile values out of the CR in the first place (and out of
// image tags, Job labels, and log lines derived from them).

// gitRefRe matches the safe subset of git ref names we accept.
//
// Deliberately narrower than git's own check-ref-format: alphanumerics
// plus . _ - / and the + sometimes used in release branches. That's
// enough for every real branch/tag name while excluding whitespace,
// quotes, backslashes, $, backticks, and shell metacharacters outright.
var gitRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+-]{0,254}$`)

// ValidateGitRef checks a branch/tag/ref name. Returns nil when safe.
//
// Beyond the charset, it enforces the git ref rules that matter for us:
// no "..", no leading/trailing "/", no "//", no trailing ".lock", and
// no "@{" sequence. A ref failing any of these is rejected rather than
// sanitized — silently rewriting a user's branch name would deploy the
// wrong code, which is worse than a clear error.
func ValidateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(ref) > 255 {
		return fmt.Errorf("must be 255 characters or fewer")
	}
	if !gitRefRe.MatchString(ref) {
		return fmt.Errorf("%q contains characters that are not allowed in a git ref "+
			"(allowed: letters, digits, and . _ - / +)", ref)
	}
	switch {
	case strings.Contains(ref, ".."):
		return fmt.Errorf("%q must not contain %q", ref, "..")
	case strings.Contains(ref, "//"):
		return fmt.Errorf("%q must not contain %q", ref, "//")
	case strings.Contains(ref, "@{"):
		return fmt.Errorf("%q must not contain %q", ref, "@{")
	case strings.HasPrefix(ref, "/"), strings.HasSuffix(ref, "/"):
		return fmt.Errorf("%q must not start or end with %q", ref, "/")
	case strings.HasSuffix(ref, ".lock"):
		return fmt.Errorf("%q must not end with %q", ref, ".lock")
	case strings.HasSuffix(ref, "."):
		return fmt.Errorf("%q must not end with %q", ref, ".")
	}
	return nil
}

// ValidateRepoURL checks a git remote URL. Returns nil when safe.
//
// Accepts http(s):// and scp-style git@host:org/repo. Rejects anything
// carrying shell metacharacters or whitespace — the clone script embeds
// the URL in a command substitution pipeline (`echo "$URL" | sed ...`),
// so a value that survives quoting today could bite a future refactor.
// file:// and ssh:// are rejected: the former would let a build read the
// builder's filesystem, and the latter isn't a supported clone path.
func ValidateRepoURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("repo URL must not be empty")
	}
	if len(raw) > 2048 {
		return fmt.Errorf("repo URL is too long")
	}
	if strings.ContainsAny(raw, " \t\r\n\"'`$;|&<>(){}[]*?!#^\\") {
		return fmt.Errorf("repo URL contains characters that are not allowed")
	}
	switch {
	case strings.HasPrefix(raw, "http://"), strings.HasPrefix(raw, "https://"):
		if len(strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")) == 0 {
			return fmt.Errorf("repo URL has no host")
		}
		return nil
	case scpStyleRepoRe.MatchString(raw):
		return nil
	}
	return fmt.Errorf("repo URL must be http(s):// or git@host:org/repo")
}

// scpStyleRepoRe matches the scp-style remote git accepts:
// user@host:path/to/repo(.git). Host and path are restricted to the
// same conservative charset as the rest of this file.
var scpStyleRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/-]+$`)
