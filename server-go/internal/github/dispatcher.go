package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// Dispatcher routes verified webhook events to their handlers. Wired
// with the projects + builds services so push/PR events can reach into
// the cluster, and (optionally) the github cache so installation +
// installation_repositories events invalidate it.
type Dispatcher struct {
	Kube       *kube.Client
	Builds     *builds.Service
	Client     *Client     // optional, for cache refresh
	Cache      CacheStore  // optional, for cache writes
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	Logger     *slog.Logger
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
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
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
		d.Logger.Info("push → trigger builds", "project", proj.Name, "branch", branch, "services", len(raw.Items))
		for i := range raw.Items {
			fqn := raw.Items[i].GetName()
			short := strings.TrimPrefix(fqn, proj.Name+"-")
			if short == "" {
				short = fqn
			}
			if d.Builds == nil {
				continue
			}
			if _, err := d.Builds.Create(ctx, proj.Name, short, builds.CreateBuildRequest{Branch: branch}); err != nil {
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
				if err := d.ensurePreviewEnv(ctx, &proj, services.Items[i].GetName(), pr); err != nil {
					d.Logger.Warn("ensure preview env", "service", services.Items[i].GetName(), "pr", pr.Number, "err", err)
				}
			}
		case "closed":
			for i := range services.Items {
				if err := d.deletePreviewEnv(ctx, proj.Name, services.Items[i].GetName(), pr.Number); err != nil {
					d.Logger.Warn("delete preview env", "service", services.Items[i].GetName(), "pr", pr.Number, "err", err)
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
	var envFromSecrets []string
	port := int32(8080)
	if svc, err := d.Kube.GetKusoService(ctx, ns, serviceFQN); err == nil && svc != nil && svc.Spec.Port > 0 {
		port = svc.Spec.Port
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
		// finalizer drama. We carry over EnvFromSecrets so addon
		// connections aren't dropped on resync.
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
