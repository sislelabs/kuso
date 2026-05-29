package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/secrets"
	"kuso/server/internal/spec"
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
	// Reconciler applies config-as-code: on a push to the default
	// branch it fetches kuso.yaml via the GitHub Contents API and
	// applies it before builds run. nil on kube-less installs — the
	// config-apply step is skipped entirely.
	Reconciler *spec.Reconciler
	// DB is the kuso server's postgres handle. Used to create
	// PreviewReview rows on PR open (v0.17.0 Phase 2). nil = reviewer
	// page integration is disabled; previews still spawn, just without
	// the public review URL.
	DB *db.DB
	// ReviewBaseURL is the prefix kuso prepends to /<token> when
	// rendering the reviewer URL in PR comments + emails. Typically
	// "https://kuso.sislelabs.com/r" (note: not /api — the page is
	// a Next.js route, the API is /api/reviews/<token>). Empty = no
	// review comment is posted.
	ReviewBaseURL string
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
		// Author identity is git-config text — a contributor can put
		// any string here. Do NOT use this for authorization or
		// audit; only the Pusher block below carries an authenticated
		// GitHub identity.
		Author struct {
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"author"`
	} `json:"head_commit"`
	// Pusher is the GitHub user who pushed the ref. GitHub vouches
	// for this — it's the OAuth identity behind the push. Use this
	// for "triggered by" attribution, never head_commit.author.
	Pusher struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
}

type prEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		State string `json:"state"`
		Title string `json:"title"`
		Body  string `json:"body"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
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
			List(ctx, metav1.ListOptions{LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: proj.Name})})
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

		// Config-as-code: fetch kuso.yaml from the repo at the pushed
		// ref and apply it before builds run. Best-effort — a parse/
		// apply error, project mismatch, or missing file logs a
		// warning and is otherwise ignored; builds still run. Guarded
		// by d.Reconciler != nil (nil on kube-less installs) and the
		// per-project configAsCode toggle.
		if d.Reconciler != nil && d.Client != nil && configAsCodeEnabled(&proj) && headSHA != "" {
			owner, repoName := splitFullName(repoFullName)
			installationID := int64(0)
			if proj.Spec.GitHub != nil {
				installationID = proj.Spec.GitHub.InstallationID
			}
			if owner == "" || repoName == "" || installationID == 0 {
				d.Logger.Warn("config-as-code skipped: missing owner/repo or installation id",
					"project", proj.Name, "repo", repoFullName)
			} else {
				fetch := func(ctx context.Context, o, r, rf, path string) ([]byte, bool, error) {
					return d.Client.GetFile(ctx, installationID, o, r, rf, path)
				}
				apply := func(ctx context.Context, parsed *spec.File) error {
					plan, err := spec.PlanFor(ctx, d.Kube, d.nsFor(ctx, proj.Name), parsed)
					if err != nil {
						return err
					}
					_, err = d.Reconciler.Apply(ctx, plan, parsed)
					return err
				}
				if err := applyConfigFromRepo(ctx, fetch, apply, owner, repoName, headSHA, proj.Name); err != nil {
					// Do NOT return — builds must still run.
					d.Logger.Warn("config-as-code apply", "project", proj.Name, "err", err)
				} else {
					d.Logger.Info("config-as-code applied", "project", proj.Name)
				}
			}
		}

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
			req := builds.CreateBuildRequest{
				Branch:      branch,
				TriggeredBy: "webhook",
				// pusher.name is the authenticated GitHub user who
				// pushed; head_commit.author.username is git-config
				// text and can be spoofed by a contributor on a
				// public repo. Never use the author field for
				// attribution.
				TriggeredByUser: p.Pusher.Name,
				CommitMessage:   firstLine(p.HeadCommit.Message),
			}
			if headSHA != "" && len(headSHA) >= 7 {
				req.Ref = headSHA
			}
			_ = prNumber // currently informational; future: stash on build label
			if _, err := d.Builds.Create(ctx, proj.Name, short, req); err != nil {
				// Dedup conflict on retried deliveries is the expected
				// path, not an error: GitHub re-fired the webhook while
				// the previous attempt was still creating the build.
				// Log at Debug so the operator's logs don't fill with
				// noise on every monorepo push.
				if errors.Is(err, builds.ErrConflict) {
					d.Logger.Debug("build trigger deduped (already in flight)",
						"project", proj.Name, "service", short, "ref", req.Ref)
				} else {
					d.Logger.Warn("build trigger", "project", proj.Name, "service", short, "err", err)
				}
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
		// Trigger gating (v0.17.0). When the project declares
		// previews.triggers[] the PR's base ref MUST match one of the
		// entries; the matched baseEnv tells us which existing env to
		// clone vars + addon subscriptions from.
		//
		// When triggers[] is empty the project is in "legacy" preview
		// mode (spawn on every PR, no explicit base mapping). v0.17.1
		// onwards default that case to `production` instead of leaving
		// baseEnv empty — the empty path caused ensurePreviewEnv to
		// skip the env-var inheritance block entirely, which left
		// previews with envVars=nil. Preview pods then ran with only
		// envFrom-mounted shared/instance/addon secrets, missing the
		// per-env valueFrom-expanded keys that the parent service's
		// SetEnv path stamped onto production. Crashlooped at startup
		// when the app asserted on missing env entries
		// (B1 from v0.17.1 PR-env audit).
		baseEnv := "production"
		if len(proj.Spec.Previews.Triggers) > 0 {
			baseEnv = ""
			matched := false
			for _, t := range proj.Spec.Previews.Triggers {
				if t.Branch == pr.PullRequest.Base.Ref {
					baseEnv = t.BaseEnv
					matched = true
					break
				}
			}
			if !matched {
				d.Logger.Info("preview skipped: PR base branch not in triggers",
					"project", proj.Name, "pr", pr.Number,
					"base", pr.PullRequest.Base.Ref)
				continue
			}
		}
		services, err := d.Kube.Dynamic.Resource(kube.GVRServices).Namespace(d.nsFor(ctx, proj.Name)).
			List(ctx, metav1.ListOptions{LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: proj.Name})})
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
				if err := d.ensurePreviewEnv(ctx, &proj, services.Items[i].GetName(), pr, baseEnv); err != nil {
					d.Logger.Warn("ensure preview env", "service", services.Items[i].GetName(), "pr", pr.Number, "err", err)
				}
			}
			// Reviewer page (v0.17.0 Phase 2). Only fires on `opened`
			// — we don't re-post the URL on every push to the same PR
			// (would spam the conversation). Idempotent at the DB
			// layer: CreatePreviewReview returns the existing row on
			// (project, prNumber) collision.
			if pr.Action == "opened" {
				d.ensureReviewerSurface(ctx, &proj, pr)
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
			// Close the reviewer row (audit history stays).
			d.closeReviewerSurface(ctx, proj.Name, pr.Number)
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
func (d *Dispatcher) ensurePreviewEnv(ctx context.Context, proj *kube.KusoProject, serviceFQN string, pr prEvent, baseEnvName string) error {
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
	envFromSecrets = append(envFromSecrets, kube.SharedSecretNames(proj.Name)...)
	port := int32(8080)
	var svcRuntime string
	var svcCommand []string
	var parentSvc *kube.KusoService
	var svcPreviewEnvVars []kube.KusoEnvVar
	var svcSharedEnvKeys []string
	var svcSubscribedAddons []string
	if svc, err := d.Kube.GetKusoService(ctx, ns, serviceFQN); err == nil && svc != nil {
		if svc.Spec.Port > 0 {
			port = svc.Spec.Port
		}
		svcRuntime = svc.Spec.Runtime
		svcCommand = svc.Spec.Command
		parentSvc = svc
		// Capture subscription state for inheritance into the preview
		// env. nil = inherit-all (legacy mount-everything); non-nil
		// passes through verbatim so the preview pod sees exactly the
		// same shared-secret keys + addon-conn secrets the production
		// pod sees.
		svcSharedEnvKeys = svc.Spec.SharedEnvKeys
		svcSubscribedAddons = svc.Spec.SubscribedAddons
		if svc.Spec.Previews != nil {
			svcPreviewEnvVars = svc.Spec.Previews.PreviewEnvVars
		}
	}
	// Inherit env vars from the matched baseEnv (post-v0.17.0). The
	// previous behaviour stamped no inline EnvVars on the preview,
	// which meant per-env URL overrides like NEXT_PUBLIC_API_URL never
	// reached the reviewer's browser. Now: clone the baseEnv's EnvVars
	// outright as the foundation. Per-service previewEnvVars overlay
	// on top so "DEMO_MODE=true" survives every resync.
	var baseEnvVars []kube.KusoEnvVar
	var baseEnvFromSecrets []string
	if baseEnvName != "" {
		baseEnvCRName := fmt.Sprintf("%s-%s", serviceFQN, baseEnvName)
		if baseEnv, err := d.Kube.GetKusoEnvironment(ctx, ns, baseEnvCRName); err == nil && baseEnv != nil {
			baseEnvVars = append(baseEnvVars, baseEnv.Spec.EnvVars...)
			// Also clone EnvFromSecrets so per-env secret mounts come
			// across. The per-env Secret names get swapped to per-PR
			// names by clonePerEnvSecretsForPreview below; shared
			// addon-conn / project-shared secret references are
			// preserved as-is (they live above env scope).
			baseEnvFromSecrets = append([]string(nil), baseEnv.Spec.EnvFromSecrets...)
			// If the service has nil SharedEnvKeys (legacy mount-all),
			// fall back to whatever the baseEnv carries explicitly so
			// the preview matches what the reviewer sees on the base
			// env URL. Same for SubscribedAddons.
			if svcSharedEnvKeys == nil && baseEnv.Spec.SharedEnvKeys != nil {
				svcSharedEnvKeys = baseEnv.Spec.SharedEnvKeys
			}
			if svcSubscribedAddons == nil && baseEnv.Spec.SubscribedAddons != nil {
				svcSubscribedAddons = baseEnv.Spec.SubscribedAddons
			}
		} else if err != nil && !apierrors.IsNotFound(err) {
			d.Logger.Warn("preview baseEnv fetch", "baseEnv", baseEnvName, "err", err)
		}
	}
	// Union the cloned baseEnv envFromSecrets with the addon-conn +
	// shared list we built up earlier. The clone goes first so the
	// per-env secret (which will be swapped to its pr-N name) is
	// mounted before the broader shared secrets — kube's envFrom
	// merge semantics make later entries win on key collision, which
	// is wrong for our case (we want per-env values to override
	// shared defaults). dedupe handles the project-shared name
	// appearing in both lists.
	envFromSecrets = dedupePreserveOrder(append(baseEnvFromSecrets, envFromSecrets...))
	// Merge in previewEnvVars: by name, preview overrides win over
	// the baseEnv copy. Empty list = no overrides (most common).
	mergedEnvVars := mergePreviewEnvVars(baseEnvVars, svcPreviewEnvVars)

	// Per-preview URL rewrite (v0.17.4). The cloned envVars carry
	// production URLs (NEXT_PUBLIC_API_URL=https://api.tickero.bg) —
	// reviewing PR-35 the browser CSP allows only the preview's own
	// connect-src, and the API on api.tickero.bg won't accept the
	// preview's auth cookies anyway. Build a {prodHost → prHost}
	// map from every service in the project, then rewrite literal
	// envVar values + per-env Secret contents.
	//
	// The map is built per call so it can't be stale across PR sync
	// events; project + service list is already cached by the
	// kube informer so this is a slice walk, not new API calls.
	hostRewrite := d.buildPreviewHostRewrite(ctx, proj, pr.Number, baseDomain)
	mergedEnvVars = rewriteEnvVarValues(mergedEnvVars, hostRewrite)

	// Clone any per-env-scoped Secret values from the baseEnv into a
	// per-PR Secret, with URL rewrites applied. This is the only way
	// preview pods get correct NEXT_PUBLIC_* values — they're set
	// per-env via the Secret, not via shared. Also swaps every
	// reference to the source Secret name (in envFromSecrets and in
	// envVars[].valueFrom.secretKeyRef.name) to point at the new
	// per-PR Secret.
	if baseEnvName != "" {
		swapped, err := d.clonePerEnvSecretsForPreview(
			ctx, ns, proj.Name, short, baseEnvName, pr.Number,
			envFromSecrets, mergedEnvVars, hostRewrite,
		)
		if err != nil {
			d.Logger.Warn("preview per-env secret clone",
				"service", short, "pr", pr.Number, "err", err)
		} else {
			envFromSecrets = swapped.envFromSecrets
			mergedEnvVars = swapped.envVars
		}
	}

	objMeta := metav1.ObjectMeta{
		Name: envName,
		Labels: map[string]string{
			"kuso.sislelabs.com/project": proj.Name,
			"kuso.sislelabs.com/service": short,
			"kuso.sislelabs.com/env":     fmt.Sprintf("preview-pr-%d", pr.Number),
		},
	}
	if parentSvc != nil {
		objMeta.OwnerReferences = []metav1.OwnerReference{kube.OwnerRefForService(parentSvc)}
	}
	env := &kube.KusoEnvironment{
		ObjectMeta: objMeta,
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
			ReplicaCount:     func() *int { v := 1; return &v }(),
			Host:             fmt.Sprintf("%s-pr-%d.%s", short, pr.Number, baseDomain),
			TLSEnabled:       true,
			ClusterIssuer:    "letsencrypt-prod",
			IngressClassName: "traefik",
			EnvFromSecrets:   envFromSecrets,
			// Cloned from baseEnv + per-service previewEnvVars overlay.
			// nil when no baseEnv was matched (triggers list empty) and
			// no previewEnvVars are defined — preserves legacy zero-
			// vars behavior for projects that haven't opted into the
			// new model.
			EnvVars: mergedEnvVars,
			// Inherit subscription state so the preview pod sees the
			// same shared-secret keys + addon-conn secrets the base
			// pod does. nil means legacy mount-all (pre-v0.16.10).
			SharedEnvKeys:    svcSharedEnvKeys,
			SubscribedAddons: svcSubscribedAddons,
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
		if _, err := d.Builds.Create(ctx, proj.Name, short, builds.CreateBuildRequest{
			Branch:          pr.PullRequest.Head.Ref,
			Ref:             pr.PullRequest.Head.SHA,
			TriggeredBy:     "webhook",
			TriggeredByUser: pr.PullRequest.User.Login,
			CommitMessage:   fmt.Sprintf("PR #%d: %s", pr.Number, pr.PullRequest.Title),
		}); err != nil {
			if errors.Is(err, builds.ErrConflict) {
				d.Logger.Debug("preview build trigger deduped (already in flight)",
					"service", serviceFQN, "pr", pr.Number)
			} else {
				d.Logger.Warn("preview build trigger", "service", serviceFQN, "pr", pr.Number, "err", err)
			}
		}
	}
	// User-defined seed command (v0.17.0 Phase 2). Runs as a one-shot
	// kube Job in a clone of the build image so it has access to the
	// app's package scripts / vendored deps. Uses the same envFromSecrets
	// + envVars the runtime pod will, so DATABASE_URL etc. resolve
	// correctly. Best-effort: a seed-job submission failure logs but
	// doesn't fail the dispatch — the preview pod still comes up;
	// reviewer sees "seed failed" on the reviewer page.
	if parentSvc != nil && parentSvc.Spec.Previews != nil && parentSvc.Spec.Previews.Seed != "" {
		buildImage := previewBuildImage(proj.Name, short, pr.PullRequest.Head.SHA)
		seedEnvVars := envVarsForSeed(mergedEnvVars)
		if err := d.runPreviewSeedJob(ctx, proj.Name, envName, buildImage, parentSvc.Spec.Previews.Seed, envFromSecrets, seedEnvVars); err != nil {
			d.Logger.Warn("preview seed job", "env", envName, "err", err)
		}
	}
	d.Logger.Info("PR preview env ready", "env", envName, "pr", pr.Number)
	return nil
}

// previewBuildImage returns the image tag the preview build will
// produce. Mirrors the convention in services_ops / builds:
//
//	kuso-registry.kuso.svc.cluster.local:5000/<project>/<service>:<sha12>
//
// The 12-char SHA prefix is what builds.ImageTag returns for a SHA-
// shaped ref. We can't call builds.ImageTag directly without an
// import cycle, so the function lives here as a one-liner mirror.
func previewBuildImage(project, service, sha string) string {
	tag := sha
	if len(sha) >= 12 {
		tag = sha[:12]
	}
	return fmt.Sprintf("kuso-registry.kuso.svc.cluster.local:5000/%s/%s:%s", project, service, tag)
}

// envVarsForSeed converts the merged baseEnv + previewEnvVars list
// (kube.KusoEnvVar shape, with map[string]any ValueFrom) into the
// preview_seed.envVar shape used to render a corev1.Container env.
// Drops entries that have neither a literal Value nor a recognisable
// secretKeyRef — the seed Job can't use them anyway.
func envVarsForSeed(in []kube.KusoEnvVar) []envVar {
	out := make([]envVar, 0, len(in))
	for _, e := range in {
		if e.Value != "" {
			out = append(out, envVar{name: e.Name, value: e.Value})
			continue
		}
		if e.ValueFrom == nil {
			continue
		}
		ref, ok := e.ValueFrom["secretKeyRef"].(map[string]any)
		if !ok {
			continue
		}
		sn, _ := ref["name"].(string)
		k, _ := ref["key"].(string)
		if sn == "" || k == "" {
			continue
		}
		out = append(out, envVar{name: e.Name, secretRef: &envVarSecretRef{secretName: sn, key: k}})
	}
	return out
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
// firstLine returns just the first newline-delimited line of a commit
// message. The deployments tab UI surfaces this in a single row so a
// multi-paragraph body would push the layout. The full message lives
// on the build CR annotation if a future drill-down wants it.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

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


// mergePreviewEnvVars overlays per-service preview overrides on top of
// the baseEnv-inherited envVars list. By name: an entry in overrides
// with the same Name as one in base replaces the base entry; net-new
// entries in overrides are appended. Empty overrides = base verbatim;
// nil base + non-empty overrides = overrides verbatim. Stable order
// matters for downstream propagation comparisons (extractEnvOnlyOverrides
// in projects.shared_env_keys does name-equality, so we preserve the
// base-then-overrides ordering instead of sorting).
func mergePreviewEnvVars(base, overrides []kube.KusoEnvVar) []kube.KusoEnvVar {
	if len(overrides) == 0 {
		return base
	}
	overrideByName := make(map[string]kube.KusoEnvVar, len(overrides))
	for _, e := range overrides {
		overrideByName[e.Name] = e
	}
	seen := make(map[string]bool, len(base)+len(overrides))
	out := make([]kube.KusoEnvVar, 0, len(base)+len(overrides))
	for _, e := range base {
		if rep, ok := overrideByName[e.Name]; ok {
			out = append(out, rep)
			seen[e.Name] = true
			continue
		}
		out = append(out, e)
		seen[e.Name] = true
	}
	for _, e := range overrides {
		if !seen[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

// ensureReviewerSurface creates a PreviewReview row (idempotent on
// project + PR number) and posts the magic-link reviewer URL as a
// GitHub PR comment. Best-effort: any failure logs but doesn't
// propagate — the preview env is already up and serving, the
// missing reviewer URL is a UX regression not a correctness one.
//
// reviewerEmail picking order: PR label `reviewer:<email>` → project
// defaultReviewerEmail → "" (no email sent; URL still posted as PR
// comment so authors with repo access can copy + send it manually).
func (d *Dispatcher) ensureReviewerSurface(ctx context.Context, proj *kube.KusoProject, pr prEvent) {
	if d.DB == nil || d.ReviewBaseURL == "" {
		return
	}
	reviewerEmail := ""
	for _, lbl := range pr.PullRequest.Labels {
		if strings.HasPrefix(lbl.Name, "reviewer:") {
			reviewerEmail = strings.TrimSpace(strings.TrimPrefix(lbl.Name, "reviewer:"))
			break
		}
	}
	if reviewerEmail == "" && proj.Spec.Previews != nil {
		reviewerEmail = proj.Spec.Previews.DefaultReviewerEmail
	}
	row, err := d.DB.CreatePreviewReview(ctx, db.PreviewReview{
		Project:       proj.Name,
		PRNumber:      pr.Number,
		PRTitle:       pr.PullRequest.Title,
		PRBody:        pr.PullRequest.Body,
		PRAuthor:      pr.PullRequest.User.Login,
		BaseRef:       pr.PullRequest.Base.Ref,
		HeadRef:       pr.PullRequest.Head.Ref,
		ReviewerEmail: reviewerEmail,
	})
	if err != nil {
		d.Logger.Warn("preview review row", "project", proj.Name, "pr", pr.Number, "err", err)
		return
	}
	if d.Client == nil {
		return
	}
	installationID := int64(0)
	if proj.Spec.GitHub != nil {
		installationID = proj.Spec.GitHub.InstallationID
	}
	if installationID == 0 {
		d.Logger.Debug("preview review: no GH installation, skipping PR comment",
			"project", proj.Name, "pr", pr.Number)
		return
	}
	// Hash-form URL (kuso/web ships under output:export which can't
	// pre-render dynamic [param] routes). The reviewer page reads the
	// hash client-side and fetches /api/reviews/<token>.
	reviewURL := strings.TrimRight(d.ReviewBaseURL, "/") + "#" + row.Token
	body := fmt.Sprintf(`🔍 **Preview ready for review**

Reviewer URL (share with the client): %s

This link lets the reviewer open the preview, leave a comment, and approve / request changes / deny without a kuso account. The decision posts back to this PR.

_Auto-generated by kuso v0.17 preview reviewer._`, reviewURL)
	if err := d.Client.PostPRComment(ctx, installationID, pr.Repository.FullName, pr.Number, body); err != nil {
		d.Logger.Warn("preview review: PR comment",
			"project", proj.Name, "pr", pr.Number, "err", err)
	}
}

// closeReviewerSurface stamps closedAt on the PreviewReview row so
// it drops out of the active-reviews list. Called from the PR-close
// branch. Doesn't delete — the row stays as audit history.
func (d *Dispatcher) closeReviewerSurface(ctx context.Context, project string, prNumber int) {
	if d.DB == nil {
		return
	}
	if err := d.DB.ClosePreviewReview(ctx, project, prNumber); err != nil {
		d.Logger.Warn("preview review close", "project", project, "pr", prNumber, "err", err)
	}
}

// ---- preview URL rewriting (v0.17.4) ------------------------------------

// buildPreviewHostRewrite walks every service in the project and
// builds a {production-host → preview-host} substitution map.
// Production hosts come from two sources, both pulled off the
// KusoService spec — no service-name guessing:
//
//  1. spec.domains[].host — every user-configured custom domain
//     (apex like "tickero.bg" or subdomain like "api.tickero.bg")
//  2. The auto-domain "<short>.<baseDomain>" — the kuso-stamped
//     production host for services without a custom domain
//
// Each production host maps to the preview's own auto-domain
// "<short>-pr-<N>.<baseDomain>". The apex case (frontend using
// "tickero.bg" with no subdomain) is handled by the custom-domain
// branch — that service has the apex on spec.domains and its
// preview's host comes through naturally. No hardcoded "frontend"
// / "web" / "www" assumption needed.
func (d *Dispatcher) buildPreviewHostRewrite(ctx context.Context, proj *kube.KusoProject, prNumber int, baseDomain string) map[string]string {
	out := map[string]string{}
	if d.Kube == nil {
		return out
	}
	ns := d.nsFor(ctx, proj.Name)
	services, err := d.Kube.Dynamic.Resource(kube.GVRServices).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: kube.LabelSelector(map[string]string{kube.LabelProject: proj.Name})})
	if err != nil {
		return out
	}
	prefix := proj.Name + "-"
	for i := range services.Items {
		u := &services.Items[i]
		fqn := u.GetName()
		short := strings.TrimPrefix(fqn, prefix)
		if short == "" {
			short = fqn
		}
		previewHost := fmt.Sprintf("%s-pr-%d.%s", short, prNumber, baseDomain)
		out[fmt.Sprintf("%s.%s", short, baseDomain)] = previewHost
		// Custom domains (spec.domains[].host). All of them, not just
		// the first — services with both an apex (tickero.bg) and a
		// www subdomain (www.tickero.bg) need both rewritten.
		// Unstructured walk so we don't need a typed decode here.
		if domains, found, _ := unstructured.NestedSlice(u.Object, "spec", "domains"); found {
			for _, dRaw := range domains {
				if dm, ok := dRaw.(map[string]any); ok {
					if host, _ := dm["host"].(string); host != "" {
						out[host] = previewHost
					}
				}
			}
		}
	}
	return out
}

// dedupePreserveOrder returns in with duplicate entries removed,
// preserving first-seen order. Used to merge per-env + project-shared
// secret name lists without changing the precedence semantics
// (later entries win on conflict in kube envFrom).
func dedupePreserveOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// rewriteHostInValue replaces every prod host (key in rewrite) with
// its preview counterpart (value) inside s. Single-pass, longest-
// match wins, host-boundary aware so "alpha.example.com" doesn't
// match inside "api.alpha.example.com" (which has its own dedicated
// rewrite entry, applied separately).
//
// Boundary rule: a host match must be preceded by a non-host char
// (start-of-string, "://", "@", ",", space) and followed by a
// non-host char (end, ":", "/", "?", "#", ",", space, quote). This
// prevents the apex baseDomain rewrite from triggering inside
// subdomain hosts.
func rewriteHostInValue(s string, rewrite map[string]string) string {
	if s == "" || len(rewrite) == 0 {
		return s
	}
	type pair struct{ from, to string }
	pairs := make([]pair, 0, len(rewrite))
	for k, v := range rewrite {
		pairs = append(pairs, pair{k, v})
	}
	// Sort by descending length so longest-host-wins inside a single
	// scan position.
	for i := 0; i < len(pairs); i++ {
		for j := i + 1; j < len(pairs); j++ {
			if len(pairs[j].from) > len(pairs[i].from) {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}
	isHostChar := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '-' || b == '.'
	}
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		matched := false
		for _, p := range pairs {
			if !strings.HasPrefix(s[i:], p.from) {
				continue
			}
			// Right boundary: char after the match must not be a host
			// char (otherwise we're matching a prefix of a longer
			// hostname like "alpha.example.com" inside
			// "api.alpha.example.com").
			end := i + len(p.from)
			if end < len(s) && isHostChar(s[end]) {
				continue
			}
			// Left boundary: char before the match must not be a host
			// char either (prevents matching a suffix inside
			// "myalpha.example.com").
			if i > 0 && isHostChar(s[i-1]) {
				continue
			}
			out.WriteString(p.to)
			i = end
			matched = true
			break
		}
		if !matched {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// rewriteEnvVarValues walks envVars and rewrites every literal value
// (entries with `Value` set; valueFrom entries are untouched — the
// rewrite for those happens via clonePerEnvSecretsForPreview's
// Secret content rewrite).
func rewriteEnvVarValues(in []kube.KusoEnvVar, rewrite map[string]string) []kube.KusoEnvVar {
	if len(in) == 0 || len(rewrite) == 0 {
		return in
	}
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		if e.Value == "" || e.ValueFrom != nil {
			out[i] = e
			continue
		}
		rewritten := rewriteHostInValue(e.Value, rewrite)
		if rewritten == e.Value {
			out[i] = e
			continue
		}
		copy := e
		copy.Value = rewritten
		out[i] = copy
	}
	return out
}

// secretSwapResult carries the rewritten envFromSecrets list and the
// envVars list with valueFrom.secretKeyRef.name pointers updated to
// the new per-PR Secret names.
type secretSwapResult struct {
	envFromSecrets []string
	envVars        []kube.KusoEnvVar
}

// clonePerEnvSecretsForPreview reads every per-env Secret that's
// scoped to the baseEnv (name pattern "<project>-<service>-<baseEnv>-secrets"),
// copies its contents to a per-PR Secret with URL rewrites applied,
// and swaps every reference to the source Secret name (in
// envFromSecrets and in envVars[].valueFrom.secretKeyRef.name) to
// point at the new per-PR Secret.
//
// Secrets that aren't per-env-scoped (project-shared, addon-conn,
// instance-shared) pass through untouched — they live above the env
// scope and don't carry env-specific URL values.
//
// Best-effort: a single Secret clone failure doesn't abort the whole
// preview spawn. The preview pod boots with whatever Secrets did
// clone successfully; missing ones surface as missing-env errors at
// app startup, which the user can fix via redeploy.
func (d *Dispatcher) clonePerEnvSecretsForPreview(
	ctx context.Context,
	ns, project, serviceShort, baseEnv string,
	prNumber int,
	envFromSecrets []string,
	envVars []kube.KusoEnvVar,
	hostRewrite map[string]string,
) (*secretSwapResult, error) {
	if d.Kube == nil || d.Kube.Clientset == nil {
		return &secretSwapResult{envFromSecrets: envFromSecrets, envVars: envVars}, nil
	}
	baseSecretName := fmt.Sprintf("%s-%s-%s-secrets", project, serviceShort, baseEnv)
	prSecretName := fmt.Sprintf("%s-%s-pr-%d-secrets", project, serviceShort, prNumber)

	src, err := d.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, baseSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// No per-env Secret on baseEnv = nothing to clone, but
			// still swap any stale envFromSecrets references in case
			// a previous spawn attempt left them dangling.
			return &secretSwapResult{
				envFromSecrets: swapSecretNameInList(envFromSecrets, baseSecretName, prSecretName),
				envVars:        swapSecretNameInEnvVars(envVars, baseSecretName, prSecretName),
			}, nil
		}
		return nil, fmt.Errorf("get source secret %s: %w", baseSecretName, err)
	}

	// Build the per-PR Secret with rewritten values. Iterating .Data
	// gives base64-decoded bytes already (k8s decodes on Get).
	prData := make(map[string][]byte, len(src.Data))
	for k, v := range src.Data {
		rewritten := rewriteHostInValue(string(v), hostRewrite)
		prData[k] = []byte(rewritten)
	}
	prSec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      prSecretName,
			Namespace: ns,
			Labels: map[string]string{
				kube.LabelProject:               project,
				"kuso.sislelabs.com/service":    serviceShort,
				"kuso.sislelabs.com/env":        fmt.Sprintf("preview-pr-%d", prNumber),
				"kuso.sislelabs.com/source-env": baseEnv,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: prData,
	}
	// Upsert: create on first spawn, patch on subsequent syncs.
	existing, gerr := d.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, prSecretName, metav1.GetOptions{})
	switch {
	case gerr != nil && apierrors.IsNotFound(gerr):
		if _, err := d.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, prSec, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create pr secret %s: %w", prSecretName, err)
		}
	case gerr != nil:
		return nil, fmt.Errorf("get existing pr secret %s: %w", prSecretName, gerr)
	default:
		existing.Data = prData
		if _, err := d.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return nil, fmt.Errorf("update pr secret %s: %w", prSecretName, err)
		}
	}

	return &secretSwapResult{
		envFromSecrets: swapSecretNameInList(envFromSecrets, baseSecretName, prSecretName),
		envVars:        swapSecretNameInEnvVars(envVars, baseSecretName, prSecretName),
	}, nil
}

// swapSecretNameInList replaces every occurrence of from with to in
// a string slice. Idempotent: a list that doesn't contain from is
// returned unchanged.
func swapSecretNameInList(in []string, from, to string) []string {
	if from == to {
		return in
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == from {
			out = append(out, to)
		} else {
			out = append(out, s)
		}
	}
	return out
}

// swapSecretNameInEnvVars rewrites envVars[i].valueFrom.secretKeyRef.name
// from→to. Other valueFrom shapes (configMapKeyRef, fieldRef) pass
// through untouched.
func swapSecretNameInEnvVars(in []kube.KusoEnvVar, from, to string) []kube.KusoEnvVar {
	if from == to || len(in) == 0 {
		return in
	}
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		if e.ValueFrom == nil {
			out[i] = e
			continue
		}
		refRaw, ok := e.ValueFrom["secretKeyRef"]
		if !ok {
			out[i] = e
			continue
		}
		refMap, ok := refRaw.(map[string]any)
		if !ok {
			out[i] = e
			continue
		}
		if name, _ := refMap["name"].(string); name == from {
			// Deep-copy the ref so we don't mutate the source map.
			newRef := map[string]any{}
			for k, v := range refMap {
				newRef[k] = v
			}
			newRef["name"] = to
			newValueFrom := map[string]any{}
			for k, v := range e.ValueFrom {
				newValueFrom[k] = v
			}
			newValueFrom["secretKeyRef"] = newRef
			copy := e
			copy.ValueFrom = newValueFrom
			out[i] = copy
			continue
		}
		out[i] = e
	}
	return out
}
