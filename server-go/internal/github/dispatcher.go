package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
	"kuso/server/internal/secrets"
)

// Dispatcher routes verified webhook events to their handlers. Wired
// with the projects + builds services so push/PR events can reach into
// the cluster, and (optionally) the github cache so installation +
// installation_repositories events invalidate it.
type Dispatcher struct {
	Kube       *kube.Client
	Builds     *builds.Service
	Secrets    *secrets.Service // optional, for per-env secret cleanup on PR close
	Client     *Client          // optional, for cache refresh
	Cache      CacheStore       // optional, for cache writes
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	Logger     *slog.Logger
	// AddonConnSecrets returns the project's addon connection-secret
	// names so previews start with envFromSecrets pre-populated for
	// every existing project addon. Without this, preview pods boot
	// without DATABASE_URL etc. and crashloop. Wired in main.go from
	// the addons service. nil = no addon attach (older behaviour).
	AddonConnSecrets func(ctx context.Context, project string) ([]string, error)
	// PreviewDB clones every postgres addon per-PR so reviewers don't
	// share production data. When wired, the clone's conn secrets
	// replace the source's in envFromSecrets. nil = previews share
	// production addons (riskier; prefer wiring this).
	PreviewDB PreviewDB
}

// PreviewDB is the surface dispatcher needs from previewdb.Cloner.
// Kept as an interface so the github package doesn't import
// previewdb directly (and so tests can stub it).
type PreviewDB interface {
	EnsurePRAddons(ctx context.Context, project string, prNumber int) ([]string, error)
	DeletePRAddons(ctx context.Context, project string, prNumber int) error
}

// nsFor returns the execution namespace for project, defaulting to home.
func (d *Dispatcher) nsFor(ctx context.Context, project string) string {
	if d.NSResolver == nil || project == "" {
		return d.Namespace
	}
	return d.NSResolver.NamespaceFor(ctx, project)
}

// NewDispatcher constructs a Dispatcher. namespace falls back to "kuso".
func NewDispatcher(k *kube.Client, b *builds.Service, namespace string, logger *slog.Logger) *Dispatcher {
	if namespace == "" {
		namespace = "kuso"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{Kube: k, Builds: b, Namespace: namespace, Logger: logger}
}

// WithGithubCache attaches the github API client + cache so the
// installation/installation_repositories webhook handlers can refresh
// the cached list on change.
func (d *Dispatcher) WithGithubCache(c *Client, cache CacheStore) *Dispatcher {
	d.Client = c
	d.Cache = cache
	return d
}

// Dispatch parses the GitHub webhook payload and routes it to the right
// handler. Unknown events log at debug and return nil.
func (d *Dispatcher) Dispatch(ctx context.Context, event string, body []byte) error {
	switch event {
	case "push":
		return d.onPush(ctx, body)
	case "pull_request":
		return d.onPullRequest(ctx, body)
	case "installation":
		return d.onInstallation(ctx, body)
	case "installation_repositories":
		return d.onInstallationRepos(ctx, body)
	default:
		d.Logger.Debug("ignoring webhook event", "event", event)
		return nil
	}
}

// pushEvent / prEvent are the slim subset of the GitHub payload we
// actually consume. Defining typed structs (rather than navigating via
// map[string]any) keeps misuse errors out at compile time.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"` // head SHA of the push (40-char hex)
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	HeadCommit struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	} `json:"head_commit"`
}

type prEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		State string `json:"state"`
		Head  struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type installationEvent struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func (d *Dispatcher) onPush(ctx context.Context, body []byte) error {
	var p pushEvent
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("decode push: %w", err)
	}
	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	repoFullName := p.Repository.FullName

	projects, err := d.Kube.ListKusoProjects(ctx, d.Namespace)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, proj := range projects {
		repoURL := ""
		defaultBranch := "main"
		if proj.Spec.DefaultRepo != nil {
			repoURL = proj.Spec.DefaultRepo.URL
			if proj.Spec.DefaultRepo.DefaultBranch != "" {
				defaultBranch = proj.Spec.DefaultRepo.DefaultBranch
			}
		}
		if !repoMatches(repoURL, repoFullName) || branch != defaultBranch {
			continue
		}
		// Trigger a build for every service in the project. Services
		// live in the project's execution namespace, which may differ
		// from the home ns when KusoProject.spec.namespace is set.
		raw, err := d.Kube.Dynamic.Resource(kube.GVRServices).Namespace(d.nsFor(ctx, proj.Name)).
			List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/project=" + proj.Name})
		if err != nil {
			d.Logger.Error("list services for push", "project", proj.Name, "err", err)
			continue
		}
		// Detect PR-merge pushes so the build name reads as
		// "pr-42-<sha>" instead of the opaque "main-<unix-ms>". GH
		// puts either "Merge pull request #N" (merge commit) or
		// "Title (#N)" (squash) in head_commit.message.
		prNumber := extractMergedPR(p.HeadCommit.Message)
		// Prefer the head_commit SHA; fall back to "after" (the
		// post-push HEAD, present on every push event).
		headSHA := p.HeadCommit.ID
		if headSHA == "" {
			headSHA = p.After
		}
		d.Logger.Info("push → trigger builds", "project", proj.Name, "branch", branch, "services", len(raw.Items), "pr", prNumber)
		for i := range raw.Items {
			fqn := raw.Items[i].GetName()
			short := strings.TrimPrefix(fqn, proj.Name+"-")
			if short == "" {
				short = fqn
			}
			if d.Builds == nil {
				continue
			}
			// For a PR-merge push, prefer the head SHA (so the build
			// CR carries a real ref instead of the synthetic
			// "<branch>-<unix-ms>"). For a regular push, also prefer
			// it. The Ref field on the request becomes the build's
			// image tag; keeping it tied to the real SHA makes
			// rollbacks pinpoint-able.
			req := builds.CreateBuildRequest{Branch: branch}
			if headSHA != "" && len(headSHA) >= 7 {
				req.Ref = headSHA
			}
			_ = prNumber // currently informational; future: stash on build label
			if _, err := d.Builds.Create(ctx, proj.Name, short, req); err != nil {
				d.Logger.Warn("build trigger", "project", proj.Name, "service", short, "err", err)
			}
		}
	}
	return nil
}

func (d *Dispatcher) onPullRequest(ctx context.Context, body []byte) error {
	var pr prEvent
	if err := json.Unmarshal(body, &pr); err != nil {
		return fmt.Errorf("decode pr: %w", err)
	}
	repoFullName := pr.Repository.FullName

	projects, err := d.Kube.ListKusoProjects(ctx, d.Namespace)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, proj := range projects {
		if proj.Spec.Previews == nil || !proj.Spec.Previews.Enabled {
			continue
		}
		repoURL := ""
		if proj.Spec.DefaultRepo != nil {
			repoURL = proj.Spec.DefaultRepo.URL
		}
		if !repoMatches(repoURL, repoFullName) {
			continue
		}
		services, err := d.Kube.Dynamic.Resource(kube.GVRServices).Namespace(d.nsFor(ctx, proj.Name)).
			List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/project=" + proj.Name})
		if err != nil {
			d.Logger.Error("list services for pr", "project", proj.Name, "err", err)
			continue
		}
		switch pr.Action {
		case "opened", "reopened", "synchronize":
			for i := range services.Items {
				// Per-service opt-out: a service can set
				// spec.previews.disabled to skip PR previews even when
				// the project toggle is on. Useful for internal
				// services (workers, cron) that have no public URL.
				if svcPreviewsDisabled(&services.Items[i]) {
					continue
				}
				if err := d.ensurePreviewEnv(ctx, &proj, services.Items[i].GetName(), pr); err != nil {
					d.Logger.Warn("ensure preview env", "service", services.Items[i].GetName(), "pr", pr.Number, "err", err)
				}
			}
		case "closed":
			for i := range services.Items {
				// Always attempt deletion on close — even for opted-out
				// services. If the user toggled the opt-out on after a
				// preview already existed, the cleanup path still
				// needs to run. d.deletePreviewEnv is idempotent.
				if err := d.deletePreviewEnv(ctx, proj.Name, services.Items[i].GetName(), pr.Number); err != nil {
					d.Logger.Warn("delete preview env", "service", services.Items[i].GetName(), "pr", pr.Number, "err", err)
				}
			}
			// Then drop every per-PR addon clone for the project.
			// Done after preview-env cleanup so the preview pod
			// terminates before the addon's conn secret vanishes
			// (avoids spurious crashloops on the way down).
			if d.PreviewDB != nil {
				if err := d.PreviewDB.DeletePRAddons(ctx, proj.Name, pr.Number); err != nil {
					d.Logger.Warn("delete pr addons", "project", proj.Name, "pr", pr.Number, "err", err)
				}
			}
		}
	}
	return nil
}

func (d *Dispatcher) onInstallation(ctx context.Context, body []byte) error {
	var ev installationEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return fmt.Errorf("decode installation: %w", err)
	}
	d.Logger.Info("installation event", "action", ev.Action, "id", ev.Installation.ID)
	if d.Cache == nil {
		return nil
	}
	if ev.Action == "deleted" {
		if err := d.Cache.Delete(ctx, ev.Installation.ID); err != nil {
			d.Logger.Warn("installation cache delete", "id", ev.Installation.ID, "err", err)
		}
		return nil
	}
	if d.Client == nil {
		return nil
	}
	if err := d.Client.RefreshInstallations(ctx, d.Cache); err != nil {
		d.Logger.Warn("installation cache refresh", "err", err)
	}
	return nil
}

func (d *Dispatcher) onInstallationRepos(ctx context.Context, body []byte) error {
	var ev installationEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return fmt.Errorf("decode installation_repositories: %w", err)
	}
	if d.Client == nil || d.Cache == nil {
		return nil
	}
	if err := d.Client.RefreshInstallationRepos(ctx, d.Cache, ev.Installation.ID); err != nil {
		d.Logger.Warn("installation repos refresh", "id", ev.Installation.ID, "err", err)
	}
	return nil
}

// svcPreviewsDisabled reads spec.previews.disabled off the unstructured
// service. We pull straight from the unstructured map rather than
// decoding the full KusoService — both are O(1) lookups but staying on
// the unstructured side keeps this file from importing the projects
// package (and the import cycle that would create).
func svcPreviewsDisabled(u *unstructured.Unstructured) bool {
	if u == nil {
		return false
	}
	disabled, found, err := unstructured.NestedBool(u.Object, "spec", "previews", "disabled")
	if err != nil || !found {
		return false
	}
	return disabled
}

// ensurePreviewEnv creates (or recreates) the preview KusoEnvironment
// for service+PR and triggers a build off the PR head ref.
func (d *Dispatcher) ensurePreviewEnv(ctx context.Context, proj *kube.KusoProject, serviceFQN string, pr prEvent) error {
	envName := fmt.Sprintf("%s-pr-%d", serviceFQN, pr.Number)
	short := strings.TrimPrefix(serviceFQN, proj.Name+"-")
	if short == "" {
		short = serviceFQN
	}
	baseDomain := proj.Spec.BaseDomain
	if baseDomain == "" {
		baseDomain = proj.Name + ".kuso.sislelabs.com"
	}
	ttlDays := 7
	if proj.Spec.Previews != nil && proj.Spec.Previews.TTLDays > 0 {
		ttlDays = proj.Spec.Previews.TTLDays
	}
	expiresAt := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour).UTC().Format(time.RFC3339)

	ns := d.nsFor(ctx, proj.Name)
	existing, err := d.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get existing env: %w", err)
	}
	// Attach addon connection secrets + project shared secret so the
	// preview pod has the same DATABASE_URL / REDIS_URL / RESEND_API_KEY
	// the production env has. Previously previews booted with no envs
	// and crashlooped on missing DATABASE_URL.
	var envFromSecrets []string
	if d.AddonConnSecrets != nil {
		if secs, err := d.AddonConnSecrets(ctx, proj.Name); err == nil {
			envFromSecrets = secs
		}
	}
	// Per-PR clones of every postgres addon. The clone's conn
	// secrets REPLACE the source's so the preview pod talks to the
	// fresh DB instead of production. Best-effort: a clone failure
	// falls back to the source secret (preview pod still boots, just
	// against shared data — same as the v0.7.0 behaviour). Non-
	// postgres addons (Redis etc.) keep the source secret regardless;
	// cloning Redis state is rarely useful.
	if d.PreviewDB != nil {
		if cloneSecrets, err := d.PreviewDB.EnsurePRAddons(ctx, proj.Name, pr.Number); err == nil {
			envFromSecrets = swapPGCloneSecrets(envFromSecrets, cloneSecrets, pr.Number)
		} else {
			d.Logger.Warn("preview db clone", "project", proj.Name, "pr", pr.Number, "err", err)
		}
	}
	envFromSecrets = append(envFromSecrets, proj.Name+"-shared", "kuso-instance-shared")
	port := int32(8080)
	var svcRuntime string
	var svcCommand []string
	if svc, err := d.Kube.GetKusoService(ctx, ns, serviceFQN); err == nil && svc != nil {
		if svc.Spec.Port > 0 {
			port = svc.Spec.Port
		}
		svcRuntime = svc.Spec.Runtime
		svcCommand = svc.Spec.Command
	}

	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: envName,
			Labels: map[string]string{
				"kuso.sislelabs.com/project": proj.Name,
				"kuso.sislelabs.com/service": short,
				"kuso.sislelabs.com/env":     fmt.Sprintf("preview-pr-%d", pr.Number),
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: proj.Name,
			Service: serviceFQN,
			Kind:    "preview",
			Branch:  pr.PullRequest.Head.Ref,
			PullRequest: &kube.KusoPullRequest{
				Number:  pr.Number,
				HeadRef: pr.PullRequest.Head.Ref,
			},
			TTL:              &kube.KusoTTL{ExpiresAt: expiresAt},
			Port:             port,
			ReplicaCount:     1,
			Host:             fmt.Sprintf("%s-pr-%d.%s", short, pr.Number, baseDomain),
			TLSEnabled:       true,
			ClusterIssuer:    "letsencrypt-prod",
			IngressClassName: "traefik",
			EnvFromSecrets:   envFromSecrets,
			// Mirror the parent service's runtime+command so worker
			// services get their proper command override on previews.
			Runtime: svcRuntime,
			Command: svcCommand,
		},
	}

	if existing != nil {
		// Update in place rather than delete + recreate. The previous
		// flow (delete, then Create) was racing with helm-operator's
		// uninstall finalizer (§6.5): delete sets deletionTimestamp,
		// helm-operator can't uninstall a non-existent helm release so
		// the finalizer stays, the next Create returns "already exists,
		// object is being deleted", and the env CR is permanently
		// stuck — preview pod terminated, no replacement spawned.
		//
		// Spec-level Update keeps the same CR alive; the operator
		// reconciles the helm release against the new values, no
		// finalizer drama. We carry over EnvFromSecrets so per-env
		// secrets the reviewer set on the preview survive a resync
		// (the shared <project>-<service>-secrets is no longer
		// auto-attached to previews; see attachToAllEnvs).
		envFromSecrets = append([]string(nil), existing.Spec.EnvFromSecrets...)
		env.Spec.EnvFromSecrets = envFromSecrets
		env.ObjectMeta.ResourceVersion = existing.ResourceVersion
		if _, err := d.Kube.UpdateKusoEnvironment(ctx, ns, env); err != nil {
			return fmt.Errorf("update preview env: %w", err)
		}
	} else {
		if _, err := d.Kube.CreateKusoEnvironment(ctx, ns, env); err != nil {
			return fmt.Errorf("create preview env: %w", err)
		}
	}
	if d.Builds != nil {
		if _, err := d.Builds.Create(ctx, proj.Name, short, builds.CreateBuildRequest{Branch: pr.PullRequest.Head.Ref, Ref: pr.PullRequest.Head.SHA}); err != nil {
			d.Logger.Warn("preview build trigger", "service", serviceFQN, "pr", pr.Number, "err", err)
		}
	}
	d.Logger.Info("PR preview env ready", "env", envName, "pr", pr.Number)
	return nil
}

func (d *Dispatcher) deletePreviewEnv(ctx context.Context, project, serviceFQN string, prNumber int) error {
	envName := fmt.Sprintf("%s-pr-%d", serviceFQN, prNumber)
	if err := d.Kube.DeleteKusoEnvironment(ctx, d.nsFor(ctx, project), envName); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete preview env %s: %w", envName, err)
	}
	// Clean up the per-env secret if the user set any vars on the
	// preview. The helm-operator finalizer tears down the helm release
	// (pods, deployment, ingress) but not the underlying Secret CR — so
	// without this, every closed PR leaves an orphan
	// <project>-<service>-pr-N-secrets behind. Best-effort: a missing
	// dep or a Kubernetes error here shouldn't block the dispatcher's
	// success path, since the env CR delete already succeeded.
	if d.Secrets != nil {
		short := strings.TrimPrefix(serviceFQN, project+"-")
		if short == "" {
			short = serviceFQN
		}
		envShort := fmt.Sprintf("pr-%d", prNumber)
		if err := d.Secrets.DeleteForEnv(ctx, project, short, envShort); err != nil {
			d.Logger.Warn("preview secret cleanup", "env", envName, "err", err)
		}
	}
	d.Logger.Info("PR preview env deleted", "env", envName, "pr", prNumber)
	return nil
}

// repoMatches checks if the configured repo URL points at the same
// owner/name as the GitHub event's full_name. We tolerate ".git"
// suffixes and case differences.
func repoMatches(configuredURL, fullName string) bool {
	if configuredURL == "" {
		return false
	}
	lower := strings.ToLower(strings.TrimSuffix(configuredURL, ".git"))
	return strings.HasSuffix(lower, "/"+strings.ToLower(fullName))
}

// extractMergedPR digs the PR number out of a merge commit message.
// GitHub uses two formats:
//   "Merge pull request #42 from owner/branch\n\n…"     (merge commit)
//   "Title of the PR (#42)\n\n…"                         (squash)
// Returns 0 when no PR number is found (e.g. a direct push to main).
func extractMergedPR(message string) int {
	if message == "" {
		return 0
	}
	// First line only — bodies can include #-references that aren't
	// the PR number.
	first := message
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		first = message[:i]
	}
	if m := mergeCommitRE.FindStringSubmatch(first); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := squashCommitRE.FindStringSubmatch(first); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

var (
	mergeCommitRE  = regexp.MustCompile(`^Merge pull request #(\d+)\b`)
	squashCommitRE = regexp.MustCompile(`\(#(\d+)\)\s*$`)
)

// swapPGCloneSecrets replaces every "<source>-conn" entry whose
// matching "<source>-pr-<N>-conn" exists in cloneSecrets. Source
// secrets without a clone (Redis etc.) are kept verbatim. The
// result preserves the source ordering of envFromSecrets.
func swapPGCloneSecrets(source []string, cloneSecrets []string, prNumber int) []string {
	if len(cloneSecrets) == 0 {
		return source
	}
	prSuffix := fmt.Sprintf("-pr-%d-conn", prNumber)
	cloneByOrigin := map[string]string{}
	for _, c := range cloneSecrets {
		// Clone secrets are named "<source>-pr-<N>-conn"; strip the
		// "-pr-<N>-conn" suffix back to the origin to build the map.
		if !strings.HasSuffix(c, prSuffix) {
			continue
		}
		origin := strings.TrimSuffix(c, prSuffix) + "-conn"
		cloneByOrigin[origin] = c
	}
	out := make([]string, len(source))
	for i, s := range source {
		if clone, ok := cloneByOrigin[s]; ok {
			out[i] = clone
		} else {
			out[i] = s
		}
	}
	return out
}
