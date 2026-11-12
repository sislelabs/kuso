package github

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
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
		if id := kube.RepoIdentity(u); id != "" {
			bound[id] = true
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
		bound["github.com/"+strings.ToLower(pushedFullName)] = true
	}
	return bound
}

// checkConfigRepoBinding refuses a kuso.yaml whose services name a repo
// outside bound, on any host: a new repo picks the code a service's
// secrets run with, App token or not (SEC-1).
func checkConfigRepoBinding(f *spec.File, bound map[string]bool) error {
	for _, s := range f.Services {
		repo, _, _ := strings.Cut(s.Repo, "#") // repo#path shorthand
		r := kube.RepoIdentity(repo)
		if r == "" {
			continue
		}
		if !bound[r] {
			return fmt.Errorf("%w: kuso.yaml service %q points at %s, which project %q doesn't use yet; add it from the dashboard or `kuso apply` as an instance admin or editor first",
				ErrConfigRepoNotBound, s.Name, r, f.Project)
		}
	}
	return nil
}

// ErrConfigRepoNotBound marks a push-applied kuso.yaml refused by the
// repo-binding gate.
var ErrConfigRepoNotBound = errors.New("config-as-code: repo not bound to project")

// ErrConfigExecNotAllowed marks a push-applied kuso.yaml refused because
// it changes how a service it doesn't build runs.
var ErrConfigExecNotAllowed = errors.New("config-as-code: execution change outside the pushed repo")

// checkConfigExecChanges refuses a push-applied kuso.yaml whose
// execution-bearing changes (command, image, release hook, build command:
// spec.Plan.ExecChanges) reach a service the pushed repo doesn't build.
// Pushing to a service's own repo already runs arbitrary code with that
// service's secrets (its Dockerfile, its start script), so kuso.yaml adds
// nothing there. For any other service, an image service above all, it
// would hand a repo pusher a capability a kuso editor no longer has
// (SEC-1); `kuso apply` by a project admin is the way to make that change.
//
// Env changes count too on a service the pushed repo doesn't build (SEC-3):
// a redirected DATABASE_URL or HTTPS_PROXY there is as much a takeover as
// a new command. And on any service, a push may not add a ${{ addon.KEY }}
// ref to an existing addon the service doesn't already use, which would
// hand the pusher that addon's credentials; an addon the same push
// creates is fine, it holds no data yet.
func checkConfigExecChanges(plan *spec.Plan, f *spec.File, proj *kube.KusoProject, svcs []unstructured.Unstructured, pushed string) error {
	if plan == nil {
		return nil
	}
	if proj == nil {
		proj = &kube.KusoProject{}
	}
	desired := make(map[string]spec.ServiceSpec, len(f.Services))
	for _, s := range f.Services {
		desired[s.Name] = s
	}
	live := make(map[string]*unstructured.Unstructured, len(svcs))
	for i := range svcs {
		live[strings.TrimPrefix(svcs[i].GetName(), f.Project+"-")] = &svcs[i]
	}
	// An existing service is judged by what it builds now, not by what
	// the push asks for: otherwise the push could repoint a service at
	// the pushed repo (or flip it off runtime=image) and then pass as
	// "built by the pushed repo" (SEC-1).
	builtByPushed := func(name string) bool {
		var runtime, repo string
		if l := live[name]; l != nil {
			runtime, _, _ = unstructured.NestedString(l.Object, "spec", "runtime")
			repo, _ = serviceEffectiveRepo(l, proj)
		} else {
			d := desired[name]
			runtime = d.Runtime
			repo, _, _ = strings.Cut(d.Repo, "#")
			if repo == "" {
				repo, _ = serviceEffectiveRepo(nil, proj)
			}
		}
		return runtime != "image" && repoMatches(repo, pushed)
	}
	newAddons := map[string]bool{}
	for _, a := range plan.AddonsToCreate {
		newAddons[a] = true
	}
	isService := func(n string) bool { _, d := desired[n]; return d || live[n] != nil }

	exec := map[string][]string{}
	var order []string
	note := func(res string, fields ...string) {
		if _, seen := exec[res]; !seen {
			order = append(order, res)
		}
		exec[res] = append(exec[res], fields...)
	}
	for _, c := range plan.ExecChanges {
		note(c.Resource, c.Fields...)
	}
	envFields := map[string][]string{}
	var badRefs []string
	refCheck := func(svc, key, value string) {
		ref, ok, _ := projects.ParseVarRef(value)
		if !ok || (projects.IsServiceKey(ref.Key) && isService(ref.Name)) || newAddons[ref.Name] ||
			serviceUsesAddon(live[svc], f.Project, ref.Name) {
			return
		}
		badRefs = append(badRefs, fmt.Sprintf("service:%s env.%s (%s)", svc, key, strings.TrimSpace(value)))
	}
	for _, c := range plan.Changes {
		name, ok := strings.CutPrefix(c.Resource, "service:")
		if !ok || c.NotApplied {
			continue
		}
		for _, fc := range c.Fields {
			if key, isEnv := strings.CutPrefix(fc.Field, "env."); isEnv {
				envFields[name] = append(envFields[name], fc.Field)
				refCheck(name, key, fc.To)
			}
		}
	}
	for _, name := range plan.ServicesToCreate {
		for key, v := range desired[name].Env {
			envFields[name] = append(envFields[name], "env."+key)
			refCheck(name, key, v.Value)
		}
	}
	for name, fields := range envFields {
		if !builtByPushed(name) {
			sort.Strings(fields)
			note("service:"+name, fields...)
		}
	}

	var refused []string
	for _, res := range order {
		name, ok := strings.CutPrefix(res, "service:")
		if !ok || !builtByPushed(name) {
			refused = append(refused, res+" ("+strings.Join(exec[res], ", ")+")")
		}
	}
	sort.Strings(badRefs)
	if len(refused) == 0 && len(badRefs) == 0 {
		return nil
	}
	var msg []string
	if len(refused) > 0 {
		msg = append(msg, fmt.Sprintf("kuso.yaml changes %s, which %s doesn't build", strings.Join(refused, "; "), pushed))
	}
	if len(badRefs) > 0 {
		msg = append(msg, "kuso.yaml wires existing addons the service doesn't use yet: "+strings.Join(badRefs, "; "))
	}
	return fmt.Errorf("%w: %s; a project admin has to apply that with `kuso apply`",
		ErrConfigExecNotAllowed, strings.Join(msg, "; "))
}

// serviceUsesAddon reports whether a live service already has access to
// the addon's connection Secret: a legacy (nil) subscription mounts every
// addon, an explicit one lists it, or an env var already refs its conn.
func serviceUsesAddon(svc *unstructured.Unstructured, project, addon string) bool {
	if svc == nil {
		return false
	}
	subs, found, _ := unstructured.NestedStringSlice(svc.Object, "spec", "subscribedAddons")
	if !found || subs == nil {
		return true
	}
	for _, s := range subs {
		if s == addon {
			return true
		}
	}
	envs, _, _ := unstructured.NestedSlice(svc.Object, "spec", "envVars")
	for _, e := range envs {
		em, _ := e.(map[string]any)
		name, _, _ := unstructured.NestedString(em, "valueFrom", "secretKeyRef", "name")
		if name == addon+"-conn" || name == project+"-"+addon+"-conn" {
			return true
		}
	}
	return false
}

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
