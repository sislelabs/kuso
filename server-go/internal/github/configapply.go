package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/spec"
)

// configFileNames are the kuso.yaml file names tried, in order, at the
// repo root. The first one that exists wins.
var configFileNames = []string{"kuso.yaml", "kuso.yml"}

// fetchFunc retrieves a file from a repo at a ref. ok=false means the
// file does not exist (a 404) — the common, non-error case.
type fetchFunc func(ctx context.Context, owner, repo, ref, path string) (content []byte, ok bool, err error)

// applyFunc plans+applies an already-parsed kuso.yaml. applyConfigFromRepo
// parses (and project-mismatch-checks) the file once and hands the
// resulting *spec.File here — the closure must not re-parse.
type applyFunc func(ctx context.Context, f *spec.File) error

// applyConfigFromRepo fetches kuso.yaml (then kuso.yml) from the repo
// at the pushed ref and applies it. A missing file is not an error.
// The file's project must match the resolved project — a mismatch is
// rejected so a webhook can never mutate a different project. The file
// is parsed exactly once here; the apply closure receives the parsed
// *spec.File.
func applyConfigFromRepo(ctx context.Context, fetch fetchFunc, apply applyFunc, owner, repo, ref, project string) error {
	var raw []byte
	var found bool
	for _, name := range configFileNames {
		content, ok, err := fetch(ctx, owner, repo, ref, name)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", name, err)
		}
		if ok {
			raw, found = content, true
			break
		}
	}
	if !found {
		return nil // no config file in the repo — nothing to do
	}
	f, err := spec.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse kuso.yaml: %w", err)
	}
	if f.Project != project {
		return fmt.Errorf("kuso.yaml project %q does not match repo's project %q", f.Project, project)
	}
	return apply(ctx, f)
}

// splitFullName splits a GitHub "owner/repo" full name into its two
// parts. Returns empty strings when the input is not in that form.
func splitFullName(fullName string) (owner, repo string) {
	parts := strings.SplitN(strings.TrimSpace(fullName), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// configBoundRepos is the set of GitHub repos ("owner/name", lower-cased)
// a push-applied kuso.yaml may point services at: the project's default
// repo, every repo its services already build from, and the pushed repo
// itself. The App token is instance-wide, so anything else would let a
// pusher of one repo get another, possibly private, repo cloned into a
// build they control.
func configBoundRepos(proj *kube.KusoProject, svcs []unstructured.Unstructured, pushedFullName string) map[string]bool {
	bound := map[string]bool{}
	add := func(u string) {
		if owner, name := ParseGithubRepoURL(u); owner != "" {
			bound[strings.ToLower(owner+"/"+name)] = true
		}
	}
	if proj != nil && proj.Spec.DefaultRepo != nil {
		add(proj.Spec.DefaultRepo.URL)
	}
	for i := range svcs {
		if u, _, _ := unstructured.NestedString(svcs[i].Object, "spec", "repo", "url"); u != "" {
			add(u)
		}
	}
	if pushedFullName != "" {
		bound[strings.ToLower(pushedFullName)] = true
	}
	return bound
}

// checkConfigRepoBinding refuses a kuso.yaml whose services name a
// GitHub repo outside bound. Non-GitHub repos pass: no App token is
// ever minted for them.
func checkConfigRepoBinding(f *spec.File, bound map[string]bool) error {
	for _, s := range f.Services {
		owner, name := ParseGithubRepoURL(s.Repo)
		if owner == "" {
			continue
		}
		if r := strings.ToLower(owner + "/" + name); !bound[r] {
			return fmt.Errorf("%w: kuso.yaml service %q points at github.com/%s, which project %q doesn't use yet; add it from the dashboard or `kuso apply` as an instance admin or editor first",
				ErrConfigRepoNotBound, s.Name, r, f.Project)
		}
	}
	return nil
}

// ErrConfigRepoNotBound marks a push-applied kuso.yaml refused by the
// repo-binding gate.
var ErrConfigRepoNotBound = errors.New("config-as-code: repo not bound to project")

// auditConfigApply writes one audit row per push-driven kuso.yaml apply.
// The pusher has no kuso account, so the row is attributed to the system
// user and names the GitHub pusher and commit in the message.
func (d *Dispatcher) auditConfigApply(ctx context.Context, project, pusher, sha, msg, severity string) {
	if d.Audit == nil {
		return
	}
	if pusher == "" {
		pusher = "unknown"
	}
	d.Audit.Log(ctx, audit.Entry{
		Severity: severity,
		Action:   "project.configAsCode.push",
		Pipeline: project,
		Resource: "kusoproject",
		Message:  fmt.Sprintf("kuso.yaml from push by GitHub user %s at %s: %s", pusher, sha, msg),
	})
}

// configPruneAllowed reports whether a push may honour `prune: true`.
// Deleting services, addons and crons from a git push is opt-in per
// project (spec.configAsCode.allowPrune); `kuso apply` is unaffected.
func configPruneAllowed(proj *kube.KusoProject) bool {
	return proj != nil && proj.Spec.ConfigAsCode != nil && proj.Spec.ConfigAsCode.AllowPrune
}

// configAsCodeEnabled reports whether the project opts into the
// kuso.yaml-on-push behaviour. Nil block = default-on.
func configAsCodeEnabled(proj *kube.KusoProject) bool {
	if proj == nil {
		return false
	}
	return proj.Spec.ConfigAsCode == nil || proj.Spec.ConfigAsCode.Enabled
}
