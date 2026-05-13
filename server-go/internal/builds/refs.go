// Pure helpers extracted from builds.go for readability. These have
// no receiver types and no state — they're string / struct shaping
// that doesn't really belong on the 2800-line builds.go entry-point
// file. Co-located here so a future contributor adding a new ref-
// shaping function (e.g. a different image-tag scheme) lands one
// place to look.
package builds

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// ImageTag returns the immutable tag we push for a build. For a full
// 40-char SHA, the leading 12 chars (matches `git rev-parse --short`).
// For an arbitrary ref string (a branch name in the dev path), the
// full string — branch names are already kube-name-safe via shortRef.
func ImageTag(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	return ref
}

// buildCRName composes the canonical KusoBuild CR name. The format
// `<project>-<service>-<short-ref>` keeps the name unique per
// (service, ref) tuple so repeated builds of the same commit collapse
// to one CR (idempotent retry without spawning duplicates).
func buildCRName(project, service, ref string) string {
	return fmt.Sprintf("%s-%s-%s", project, service, shortRef(ref))
}

// shortRef trims a 40-char SHA to its 12-char short form, or
// slugifies an arbitrary ref string to a kube-name-safe slice
// (lowercase letters/digits/dashes, ≤32 chars so the full build
// name stays under 63).
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
