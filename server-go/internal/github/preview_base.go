package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/addons"
	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
	"kuso/server/internal/version"
)

// errNoStagingEnv means a service has no env a preview may be based on.
// Previews never inherit from production: its per-env Secret holds the live
// payment keys and webhook secrets a PR author must not read (ISO-1).
var errNoStagingEnv = errors.New("no staging environment")

// defaultPreviewBase is the env a preview inherits from when no trigger
// names one.
const defaultPreviewBase = "staging"

// previewBase is the non-production env a preview copies its env vars,
// per-env secrets, addon subscriptions and seed data from.
type previewBase struct {
	Env *kube.KusoEnvironment
	// Scope is the env label of the base ("staging"): the addon-clone scope
	// whose postgres seeds the preview's clone.
	Scope string
	// Secrets are the base env's own Secrets, merged in order (later wins)
	// into the preview's per-PR Secret.
	Secrets []string
}

// previewBaseName resolves the env name a preview of a PR inherits from.
// A trigger's baseEnv is honoured unless it names production.
func previewBaseName(requested string) string {
	if requested == "" || requested == "production" {
		return defaultPreviewBase
	}
	return requested
}

// resolvePreviewBase finds the base env for a preview of serviceFQN. The
// base is either a plain env of the service ("<fqn>-staging") or the env of
// the service's env-group clone ("<fqn>-staging" service, env labelled
// env=staging). When both exist the one tracking baseRef wins, else the
// plain env. errNoStagingEnv when neither exists.
func (d *Dispatcher) resolvePreviewBase(ctx context.Context, ns, project, serviceFQN, baseRef, requested string) (*previewBase, error) {
	name := previewBaseName(requested)
	short := strings.TrimPrefix(serviceFQN, project+"-")
	envs, err := d.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelEnv:     name,
	})
	if err != nil {
		return nil, fmt.Errorf("list %s envs: %w", name, err)
	}
	isBase := func(e *kube.KusoEnvironment) bool {
		return e.Spec.Project == project && !addons.IsPreviewEnv(e) && e.DeletionTimestamp == nil
	}
	var plain, group *kube.KusoEnvironment
	for i := range envs {
		e := &envs[i]
		if !isBase(e) {
			continue
		}
		switch e.Spec.Service {
		case serviceFQN:
			plain = e
		case serviceFQN + "-" + name:
			group = e
		}
	}
	if plain == nil {
		// Unlabelled legacy env CR named <fqn>-<name>.
		e, gerr := d.Kube.GetKusoEnvironment(ctx, ns, serviceFQN+"-"+name)
		switch {
		case gerr == nil && e != nil && e.Spec.Service == serviceFQN && isBase(e):
			plain = e
		case gerr != nil && !apierrors.IsNotFound(gerr):
			return nil, fmt.Errorf("get env %s-%s: %w", serviceFQN, name, gerr)
		}
	}
	switch {
	case plain != nil && (group == nil || plain.Spec.Branch == baseRef || group.Spec.Branch != baseRef):
		return &previewBase{Env: plain, Scope: name, Secrets: []string{kube.EnvSecretName(project, short, name)}}, nil
	case group != nil:
		clone := short + "-" + name
		return &previewBase{Env: group, Scope: name, Secrets: []string{
			kube.ServiceSecretName(project, clone),
			kube.EnvSecretName(project, clone, name),
		}}, nil
	}
	return nil, fmt.Errorf("%w %q for %s", errNoStagingEnv, name, short)
}

// previewStatusContext is the commit-status context a refused preview is
// reported under. kuso/-prefixed statuses never count toward CI verdicts.
const previewStatusContext = "kuso/preview"

// previewSkipMessage is the one-line reason a preview was refused.
func previewSkipMessage(baseName string, services []string, wholeProject bool) string {
	if wholeProject {
		return fmt.Sprintf("preview skipped: project has no %s environment", baseName)
	}
	return fmt.Sprintf("preview skipped for %s: no %s environment", strings.Join(services, ", "), baseName)
}

// reportPreviewSkipped makes a preview refused for lack of a staging env
// visible: a failing kuso/preview commit status on the PR head (every
// push), and once per PR a notification (bell feed + webhooks) and, when no
// preview came up at all, a PR comment.
func (d *Dispatcher) reportPreviewSkipped(ctx context.Context, proj *kube.KusoProject, pr prEvent, baseName string, services []string, wholeProject bool) {
	msg := previewSkipMessage(baseName, services, wholeProject)
	d.Logger.Info(msg, "project", proj.Name, "pr", pr.Number)
	installationID := int64(0)
	if proj.Spec.GitHub != nil {
		installationID = proj.Spec.GitHub.InstallationID
	}
	owner, repo, _ := strings.Cut(pr.Repository.FullName, "/")
	if d.Client != nil && installationID != 0 && pr.PullRequest.Head.SHA != "" && repo != "" {
		if err := d.Client.PostCommitStatus(ctx, installationID, owner, repo, pr.PullRequest.Head.SHA, CommitStatusInput{
			State:       CIStateFailure,
			Context:     previewStatusContext,
			Description: truncateStatus(msg),
		}); err != nil {
			d.Logger.Warn("preview skipped: commit status", "project", proj.Name, "pr", pr.Number, "err", err)
		}
	}
	key := fmt.Sprintf("%s/%s#%d", proj.Name, pr.Repository.FullName, pr.Number)
	d.closedMu.Lock()
	if d.skipReported == nil {
		d.skipReported = map[string]bool{}
	}
	first := !d.skipReported[key]
	d.skipReported[key] = true
	d.closedMu.Unlock()
	if !first {
		return
	}
	if d.Notifier != nil {
		d.Notifier.Emit(builds.EventEnvelope{
			Type:  "build.failed",
			Title: fmt.Sprintf("Preview for PR #%d not deployed: %s", pr.Number, proj.Name),
			Body: msg + ". Previews copy their config and secrets from a " + baseName +
				" environment, never from production. Create one (kuso env create, or a " + baseName +
				" environment group) and push to the PR again.",
			Project:  proj.Name,
			Severity: "warn",
		})
	}
	if wholeProject && d.Client != nil && installationID != 0 &&
		(pr.Action == "opened" || pr.Action == "reopened") {
		body := fmt.Sprintf("**%s.**\n\nkuso previews copy their config and secrets from a `%s` environment and never from production. "+
			"Add a `%s` environment to the project, then push to this PR to get a preview.\n\n_kuso %s_",
			strings.ToUpper(msg[:1])+msg[1:], baseName, baseName, version.Version())
		if err := d.Client.PostPRComment(ctx, installationID, pr.Repository.FullName, pr.Number, body); err != nil {
			d.Logger.Warn("preview skipped: PR comment", "project", proj.Name, "pr", pr.Number, "err", err)
		}
	}
}

// truncateStatus fits GitHub's 140-character status description limit.
func truncateStatus(s string) string {
	if len(s) <= 140 {
		return s
	}
	return s[:137] + "..."
}

// stagingConnSwaps maps the base env's own addon clones onto what a preview
// mounts instead, and lists the ones it must not mount:
//   - a base clone whose source has a per-PR clone (postgres/redis) → the
//     per-PR clone; the preview never shares staging's database;
//   - a postgres/redis base clone without one → withheld;
//   - any other base clone (s3, …) → kept, and the production conn it
//     replaces is swapped to it, so a preview uses staging's bucket rather
//     than production's (ISO-4).
//
// prClones is the per-PR source→clone map. Clones are paired with their
// source by recorded provenance only.
func stagingConnSwaps(baseClones []kube.KusoAddon, project string, prClones map[string]string) (swap map[string]string, withhold []string) {
	swap = map[string]string{}
	for i := range baseClones {
		a := &baseClones[i]
		conn := addons.ConnSecretName(a.Name)
		src := recordedCloneSource(a, project)
		if pr, ok := prClones[src]; ok && src != "" {
			swap[conn] = pr
			continue
		}
		if a.Spec.Kind == "postgres" || a.Spec.Kind == "redis" {
			withhold = append(withhold, conn)
			continue
		}
		if src != "" {
			swap[src] = conn
		}
	}
	slices.Sort(withhold)
	return swap, withhold
}

// recordedCloneSource is the project-level conn a clone addon was minted
// from (preview-source label or env-group-source-addon annotation); "" when
// neither was recorded. Mirrors projects.recordedCloneSource.
func recordedCloneSource(a *kube.KusoAddon, project string) string {
	if short := a.Labels["kuso.sislelabs.com/preview-source"]; short != "" {
		if !strings.HasPrefix(short, project+"-") {
			short = project + "-" + short
		}
		return addons.ConnSecretName(short)
	}
	if fqn := a.Annotations["kuso.sislelabs.com/env-group-source-addon"]; fqn != "" {
		return addons.ConnSecretName(fqn)
	}
	return ""
}
