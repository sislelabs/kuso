// Package builds owns the KusoBuild CR lifecycle: list/get/create plus a
// status poller that watches the rendered Job (via batch/v1) and
// promotes the image tag onto the production env on success.
//
// Phase 5 ships build creation that accepts an explicit ref or a public-
// repo branch (synthetic ref). Branch → SHA resolution via the GitHub
// App lands in Phase 6 once the github package is wired.
package builds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// TokenMinter mints a short-lived GitHub installation token used by the
// build's clone init container. The builds package depends on the
// interface (not the github package directly) so the github client
// stays optional — when it's nil, builds still go out, but with an
// empty token (the clone will fail clearly instead of pods wedging on
// "secret not found").
type TokenMinter interface {
	MintInstallationToken(ctx context.Context, installationID int64) (string, error)
}

// RegistryHost is the in-cluster registry every build pushes to. The
// helm chart for kuso-registry exposes this as a Service.
const RegistryHost = "kuso-registry.kuso.svc.cluster.local:5000"

// Build phase + timing live on annotations because helm-operator owns
// .status on every CR and overwrites the whole stanza on each
// reconcile. Keys are namespaced under kuso.sislelabs.com/build-* so
// they don't collide with anything else that ends up on the object.
const (
	annPhase       = "kuso.sislelabs.com/build-phase"
	annCompletedAt = "kuso.sislelabs.com/build-completed-at"
	annStartedAt   = "kuso.sislelabs.com/build-started-at"
	annMessage     = "kuso.sislelabs.com/build-message"
)

// buildPhase returns the kuso-tracked phase from build annotations,
// falling back to the legacy .status.phase for builds created before
// v0.6.3 (their annotation slot is empty; .status.phase may still be
// set if helm-operator hadn't re-reconciled yet).
func buildPhase(b *kube.KusoBuild) string {
	if b == nil {
		return ""
	}
	if v, ok := b.Annotations[annPhase]; ok && v != "" {
		return v
	}
	if b.Status != nil {
		if s, ok := b.Status["phase"].(string); ok {
			return s
		}
	}
	return ""
}

// Service handles the build domain. Construct via New.
type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
	// Tokens mints fresh github installation tokens for the build's
	// clone init container. Optional — nil means we still create the
	// expected secret (empty value) so pods start, but private repos
	// will fail to clone.
	Tokens TokenMinter

	// inFlight dedupes concurrent Create calls keyed on
	// (project, service, sha). When a webhook retries (GitHub does so
	// on 5xx) or two callers race the same build, the second caller
	// waits on the first's outcome instead of creating a duplicate
	// KusoBuild + clone-token Secret. Keys live for the duration of
	// Create only; freed in defer regardless of outcome.
	inFlight sync.Map
}

// inFlightEntry is the value side of inFlight: a channel that closes
// when the leader's Create returns, plus its result. Followers block
// on done and read result/err. We don't bother coalescing the response
// (returning the leader's CR pointer to followers) because Create's
// caller treats the body as advisory — what matters is that we don't
// spawn a second build pod for the same SHA.
type inFlightEntry struct {
	done chan struct{}
}

// inFlightKey produces the dedup key. SHA is the natural primary key
// for a build (image tag derives from it); project + service prevent
// false-positive collisions when two services in different projects
// happen to share a SHA (rare but possible with monorepos).
func inFlightKey(project, service, sha string) string {
	return project + "/" + service + "/" + sha
}

// New constructs a builds.Service with a default namespace fallback.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// nsFor returns the execution namespace for project, defaulting to the
// home Namespace.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// scanNamespaces returns every namespace the build poller / promotion
// flow needs to walk: the home ns plus every distinct spec.namespace
// declared by a KusoProject. Deduped, errors swallowed (always at
// least the home ns is returned).
func (s *Service) scanNamespaces(ctx context.Context) []string {
	out := []string{s.Namespace}
	seen := map[string]bool{s.Namespace: true}
	if s.Kube == nil {
		return out
	}
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return out
	}
	for _, p := range projects {
		ns := p.Spec.Namespace
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	return out
}

// Errors mirroring the rest of the codebase.
var (
	ErrNotFound = errors.New("builds: not found")
	ErrInvalid  = errors.New("builds: invalid")
	// ErrConflict is returned when an identical build (same project,
	// service, and resolved SHA) is already being created. The HTTP
	// layer maps this to 409; the GitHub webhook dispatcher swallows
	// it because a retried delivery hitting an in-flight build is a
	// success, not a failure.
	ErrConflict = errors.New("builds: conflict")
)

// CreateBuildRequest is the body of POST /api/projects/:p/services/:s/builds.
type CreateBuildRequest struct {
	Branch string `json:"branch,omitempty"`
	Ref    string `json:"ref,omitempty"`
}

// shaRE matches a full 40-char git SHA.
var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// List returns the builds for a project (and optionally for a single
// service inside it), newest first.
func (s *Service) List(ctx context.Context, project, service string) ([]kube.KusoBuild, error) {
	selector := "kuso.sislelabs.com/project=" + project
	if service != "" {
		selector += ",kuso.sislelabs.com/service=" + project + "-" + service
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(s.nsFor(ctx, project)).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list builds: %w", err)
	}
	out := make([]kube.KusoBuild, 0, len(raw.Items))
	for i := range raw.Items {
		var b kube.KusoBuild
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
			return nil, fmt.Errorf("decode build: %w", err)
		}
		out = append(out, b)
	}
	// Newest first by creationTimestamp; ties break on name lexically so
	// the order is stable across calls.
	sort.SliceStable(out, func(i, j int) bool {
		ti := out[i].CreationTimestamp
		tj := out[j].CreationTimestamp
		if ti.Equal(&tj) {
			return out[i].Name > out[j].Name
		}
		return ti.After(tj.Time)
	})
	return out, nil
}

// Create persists a new KusoBuild. Phase 5 limits:
//   - explicit `ref` (40-char SHA) → image tag = first 12 chars
//   - explicit `ref` (anything else) → image tag = ref verbatim
//   - empty ref + branch + no GitHub installation → synthetic
//     "<branch>-<unix-millis>" tag (kaniko clones HEAD of the branch)
//   - branch → SHA via GitHub App is Phase 6
//
// The KusoBuild CR carries the resolved image repository + tag. The
// operator's helm-charts/kusobuild chart renders the kaniko Job; the
// poller picks up its outcome.
func (s *Service) Create(ctx context.Context, project, service string, req CreateBuildRequest) (*kube.KusoBuild, error) {
	fqn := project + "-" + service
	ns := s.nsFor(ctx, project)
	svcCR, err := s.Kube.GetKusoService(ctx, ns, fqn)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: service %s/%s", ErrNotFound, project, service)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight service: %w", err)
	}
	// KusoProject CR always lives in the home namespace.
	proj, err := s.Kube.GetKusoProject(ctx, s.Namespace, project)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: project %s", ErrNotFound, project)
	}
	if err != nil {
		return nil, fmt.Errorf("preflight project: %w", err)
	}

	repoURL := ""
	repoPath := "."
	if svcCR.Spec.Repo != nil {
		repoURL = svcCR.Spec.Repo.URL
		if svcCR.Spec.Repo.Path != "" {
			repoPath = svcCR.Spec.Repo.Path
		}
	}
	if repoURL == "" && proj.Spec.DefaultRepo != nil {
		repoURL = proj.Spec.DefaultRepo.URL
	}
	if repoURL == "" {
		return nil, fmt.Errorf("%w: service has no repo URL configured", ErrInvalid)
	}

	branch := req.Branch
	if branch == "" {
		if proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
			branch = proj.Spec.DefaultRepo.DefaultBranch
		} else {
			branch = "main"
		}
	}

	sha := req.Ref
	syntheticRef := !shaRE.MatchString(sha)
	if syntheticRef {
		// Phase 5 cannot resolve branch → SHA via GitHub yet. Synthesize
		// a unique-ish ref. Phase 6 will replace this branch with the
		// real github resolve.
		sha = fmt.Sprintf("%s-%s", branch, strconv.FormatInt(time.Now().UnixMilli(), 36))
	}

	// Dedup concurrent Create calls for the same (project, service, sha).
	// A retried GitHub webhook (or two webhooks racing the same push) used
	// to spawn duplicate KusoBuild + clone-token secrets — kaniko would
	// fail on the second one with "secret already exists" or worse, both
	// would race to push the same image tag. We only dedup on real SHAs;
	// synthetic refs already carry a unix-ms suffix so they're already
	// unique-by-construction.
	if !syntheticRef {
		key := inFlightKey(project, service, sha)
		entry := &inFlightEntry{done: make(chan struct{})}
		if existing, loaded := s.inFlight.LoadOrStore(key, entry); loaded {
			// Another goroutine is already creating this build. Wait for
			// it and return Conflict so the caller treats this as "no-op,
			// already running" — which is exactly what GitHub's retry
			// path needs.
			prev := existing.(*inFlightEntry)
			select {
			case <-prev.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("%w: build for %s/%s@%s already in flight",
				ErrConflict, project, service, shortRef(sha))
		}
		defer func() {
			s.inFlight.Delete(key)
			close(entry.done)
		}()
	}

	buildName := buildCRName(project, service, sha)
	imageRepo := fmt.Sprintf("%s/%s/%s", RegistryHost, project, service)

	// Strategy mirrors KusoService.spec.runtime. The chart switches on
	// `strategy: dockerfile|nixpacks` to pick the kaniko args + the
	// optional nixpacks-plan init container. Empty defaults to dockerfile.
	strategy := svcCR.Spec.Runtime
	if strategy == "" {
		strategy = "dockerfile"
	}

	installationID := githubInstallationID(proj)

	// The chart's clone init container reads $GITHUB_INSTALLATION_TOKEN
	// from a secret named "<release>-token" with key "token". The
	// release name == the KusoBuild CR name. Mint a fresh installation
	// token and write the secret BEFORE creating the CR so the operator's
	// helm render finds it the moment the Job pod schedules.
	if err := s.ensureCloneTokenSecret(ctx, ns, buildName, installationID); err != nil {
		return nil, fmt.Errorf("clone token secret: %w", err)
	}

	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: buildName,
			Labels: map[string]string{
				"kuso.sislelabs.com/project":   project,
				"kuso.sislelabs.com/service":   fqn,
				"kuso.sislelabs.com/build-ref": shortRef(sha),
			},
		},
		Spec: kube.KusoBuildSpec{
			Project:              project,
			Service:              fqn,
			Ref:                  sha,
			Branch:               branch,
			Repo:                 &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
			GithubInstallationID: installationID,
			Strategy:             strategy,
			Image:                &kube.KusoImage{Repository: imageRepo, Tag: ImageTag(sha)},
			// Carry strategy-specific configuration from the service
			// spec onto the build CR so the helm chart can render the
			// right command line. Empty pointers leave the chart on
			// its defaults.
			Static:     svcCR.Spec.Static,
			Buildpacks: svcCR.Spec.Buildpacks,
		},
	}
	return s.Kube.CreateKusoBuild(ctx, ns, build)
}

// Rollback re-points the production env at a previous build's image
// tag. The build must be in phase=succeeded — rolling to a failed
// build would land a broken pod. Returns the patched env.
func (s *Service) Rollback(ctx context.Context, project, service, buildName string) (*kube.KusoEnvironment, error) {
	ns := s.nsFor(ctx, project)
	bRaw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, buildName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get build: %w", err)
	}
	var b kube.KusoBuild
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(bRaw.Object, &b); err != nil {
		return nil, fmt.Errorf("decode build: %w", err)
	}
	if buildPhase(&b) != "succeeded" {
		return nil, fmt.Errorf("build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", buildName, buildPhase(&b))
	}
	if b.Spec.Image == nil {
		return nil, fmt.Errorf("build %s has no image to roll back to", buildName)
	}
	// Patch the production env's image to the build's image. Same
	// shape as the Poller's promoteImage path.
	envName := project + "-" + service + "-production"
	patch := fmt.Sprintf(`{"spec":{"image":{"repository":%q,"tag":%q,"pullPolicy":"IfNotPresent"}}}`,
		b.Spec.Image.Repository, b.Spec.Image.Tag)
	if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, envName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch env %s: %w", envName, err)
	}
	envRaw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("re-read env: %w", err)
	}
	var e kube.KusoEnvironment
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(envRaw.Object, &e)
	return &e, nil
}

// ensureCloneTokenSecret upserts the <buildName>-token Secret used by
// the clone init container. We mint a fresh installation token when
// the github client is wired AND an installation id is set; otherwise
// we still write a secret (with empty token) so pods can start and
// surface a clean clone error instead of wedging on
// CreateContainerConfigError.
func (s *Service) ensureCloneTokenSecret(ctx context.Context, ns, buildName string, installationID int64) error {
	token := ""
	if s.Tokens != nil && installationID > 0 {
		t, err := s.Tokens.MintInstallationToken(ctx, installationID)
		if err != nil {
			return fmt.Errorf("mint installation token: %w", err)
		}
		token = t
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildName + "-token",
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/build":     buildName,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"token": token},
	}
	_, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		// Refresh in place — token is short-lived, so reusing a stale
		// one risks a clone failure on retry.
		_, uerr := s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
		return uerr
	}
	return err
}

// ImageTag returns the canonical image tag for a ref: 12-char SHA prefix
// for full SHAs, otherwise the ref verbatim. Exported for the GitHub
// webhook handler in Phase 6.
func ImageTag(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	return ref
}

func buildCRName(project, service, ref string) string {
	return fmt.Sprintf("%s-%s-%s", project, service, shortRef(ref))
}

func shortRef(ref string) string {
	if shaRE.MatchString(ref) {
		return ref[:12]
	}
	// Trim to a k8s-name-safe slug. Replace anything not [a-z0-9-] with
	// dashes and clip to 32 chars so the full build name stays under 63.
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

func githubInstallationID(proj *kube.KusoProject) int64 {
	if proj == nil || proj.Spec.GitHub == nil {
		return 0
	}
	return proj.Spec.GitHub.InstallationID
}

// ---- Status poller -------------------------------------------------------

// Poller watches kaniko Jobs rendered for KusoBuilds and stamps their
// outcome onto KusoBuild.status. On success it patches the production
// KusoEnvironment with the new image tag.
// EventEmitter is the (notify.Dispatcher.Emit) signature the poller
// calls when a build transitions. Kept as an interface here so the
// builds package doesn't pull in notify (avoids an import cycle if
// notify ever wants build types). Nil emitter = silent.
type EventEmitter interface {
	Emit(e EventEnvelope)
}

// EventEnvelope is the minimum payload a notify dispatcher needs.
// Mirrors notify.Event's interesting fields without the import.
type EventEnvelope struct {
	Type     string
	Title    string
	Body     string
	Project  string
	Service  string
	URL      string
	Severity string
	Extra    map[string]string
}

type Poller struct {
	Svc      *Service
	Interval time.Duration
	Logger   *slog.Logger
	// Notifier receives build.{started,succeeded,failed} events.
	// Optional: nil → no notifications.
	Notifier EventEmitter
}

// Run blocks until ctx is cancelled, ticking every Interval and updating
// any KusoBuild whose phase is not yet succeeded/failed. Returns ctx.Err
// on shutdown. Errors from individual ticks are logged at warn so we
// never silently lose state changes — the previous "_ = err" silenced a
// real bug for an entire test cycle.
func (p *Poller) Run(ctx context.Context) error {
	if p.Interval <= 0 {
		p.Interval = 30 * time.Second
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	tick := time.NewTicker(p.Interval)
	defer tick.Stop()
	for {
		if err := p.tick(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.Logger.Warn("build poller tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (p *Poller) tick(ctx context.Context) error {
	// Walk every project's execution namespace, deduped + including the
	// home namespace so single-tenant clusters keep working.
	//
	// We exclude builds the operator already considers terminal
	// (build-state=done). The label is stamped by markSucceeded /
	// markFailed below and is the same selector the helm-operator
	// watch (operator/watches.yaml) uses to skip reconciliation. With
	// 1000 historical builds in a busy cluster, this turns a full-table
	// list into an indexed scan over the few in-flight rows — and
	// matches what the operator side already does.
	const activeBuilds = "!kuso.sislelabs.com/build-state"
	for _, ns := range p.Svc.scanNamespaces(ctx) {
		raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			List(ctx, metav1.ListOptions{LabelSelector: activeBuilds})
		if err != nil {
			p.Logger.Warn("build poller list", "ns", ns, "err", err)
			continue
		}
		for i := range raw.Items {
			var b kube.KusoBuild
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
				continue
			}
			// Defensive: the selector excludes done builds, but a
			// build mid-mark could land here on a race. Keep the
			// in-memory phase check so we skip it cleanly.
			phase := buildPhase(&b)
			if phase == "succeeded" || phase == "failed" {
				continue
			}
			if err := p.checkBuild(ctx, ns, &b); err != nil && !apierrors.IsNotFound(err) {
				p.Logger.Warn("build poller checkBuild", "build", b.Name, "ns", ns, "err", err)
			}
		}
	}
	return nil
}

// checkBuild reads the kaniko Job for one build and reconciles status.
// ns is the namespace the KusoBuild + Job live in (determined by the
// project's spec.namespace, looked up by the caller).
func (p *Poller) checkBuild(ctx context.Context, ns string, b *kube.KusoBuild) error {
	job, err := p.Svc.Kube.Clientset.BatchV1().Jobs(ns).Get(ctx, b.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cond := completedCondition(job); cond != nil {
		if cond.Type == batchv1.JobComplete {
			return p.markSucceeded(ctx, ns, b)
		}
		return p.markFailed(ctx, ns, b, cond.Message)
	}
	if job.Status.Active > 0 {
		return p.markRunning(ctx, ns, b)
	}
	return nil
}

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

// Build phase + timing live on annotations, not .status. helm-operator
// owns .status on every CR it manages (it overwrites the whole stanza
// with conditions + deployedRelease on each reconcile, every 1m for
// kusobuilds), so any .status.phase we write gets obliterated within
// a minute. Annotations are part of metadata, ignored by helm-operator,
// and we read them back the same way we used to read .status.phase.
//
// We also stamp a label `kuso.sislelabs.com/build-state=done` so the
// helm-operator's watch selector (operator/watches.yaml) excludes the
// CR from further reconciles. Without it, completed KusoBuilds get
// reconciled every 60s forever — burning ~10% of one core in a busy
// cluster on dead work.
func (p *Poller) markSucceeded(ctx context.Context, ns string, b *kube.KusoBuild) error {
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"succeeded",%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}}}`,
		annPhase, annCompletedAt, time.Now().UTC().Format(time.RFC3339),
	)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch build status: %w", err)
	}
	if p.Notifier != nil {
		ref := b.Spec.Ref
		if len(ref) > 12 {
			ref = ref[:12]
		}
		p.Notifier.Emit(EventEnvelope{
			Type:     "build.succeeded",
			Title:    fmt.Sprintf("✓ Build succeeded: %s", b.Spec.Service),
			Body:     fmt.Sprintf("ref `%s` on `%s`", ref, b.Spec.Branch),
			Project:  b.Spec.Project,
			Service:  b.Spec.Service,
			Severity: "info",
		})
	}
	return p.promoteImage(ctx, ns, b)
}

func (p *Poller) markFailed(ctx context.Context, ns string, b *kube.KusoBuild, msg string) error {
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"failed",%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}}}`,
		annPhase, annCompletedAt, time.Now().UTC().Format(time.RFC3339),
		annMessage, msg,
	)
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build failed: %w", err)
	}
	if p.Notifier != nil {
		ref := b.Spec.Ref
		if len(ref) > 12 {
			ref = ref[:12]
		}
		p.Notifier.Emit(EventEnvelope{
			Type:     "build.failed",
			Title:    fmt.Sprintf("✗ Build failed: %s", b.Spec.Service),
			Body:     msg,
			Project:  b.Spec.Project,
			Service:  b.Spec.Service,
			Severity: "error",
			Extra:    map[string]string{"ref": ref, "branch": b.Spec.Branch},
		})
	}
	return nil
}

func (p *Poller) markRunning(ctx context.Context, ns string, b *kube.KusoBuild) error {
	if buildPhase(b) == "running" {
		return nil
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:"running"}}}`, annPhase))
	_, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch build running: %w", err)
	}
	return nil
}

// logger returns the configured Logger or slog.Default(). Used so the
// poller's helper paths work in unit tests that construct a Poller
// directly without going through Run().
func (p *Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// promoteImage patches the new image tag onto every KusoEnvironment
// whose branch matches the build's branch. That's:
//
//   - the production env when builds are triggered against the project
//     default branch (whether by a push webhook or a manual `build trigger`);
//   - the preview-pr-N env when builds are triggered against a PR head
//     ref (via the dispatcher's onPullRequest path);
//   - both envs when somebody manually triggers a build with the same
//     branch as the PR they're testing — that's a no-op for production
//     in practice because webhook flows separate the two.
//
// The TS server only patched production; preview envs sat at
// InvalidImageName until a human manually edited spec.image. Per the
// rewrite plan §5 / TS comment in github-webhooks.service.ts, that was
// a known-incomplete feature. We close it here by matching on
// spec.branch over the env list filtered to this build's service.
func (p *Poller) promoteImage(ctx context.Context, ns string, b *kube.KusoBuild) error {
	if b.Spec.Image == nil {
		return nil
	}
	// List only envs that belong to this build's project + service.
	// Without the selector, every promotion walks every env in the
	// namespace — fine at 5 envs, painful at 50 (preview-env explosion
	// path).
	//
	// Env labels carry the SHORT service name (req.Name), but
	// b.Spec.Service is the FQN form "<project>-<service>". Strip the
	// project prefix so the selector matches the env's labels.
	// The in-memory `e.Spec.Service != b.Spec.Service` check below is
	// a belt-and-braces fallback for older envs missing the label.
	shortService := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
	if shortService == "" {
		shortService = b.Spec.Service
	}
	selector := "kuso.sislelabs.com/project=" + b.Spec.Project +
		",kuso.sislelabs.com/service=" + shortService
	raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list envs for promotion: %w", err)
	}
	patch := fmt.Sprintf(`{"spec":{"image":{"repository":%q,"tag":%q,"pullPolicy":"IfNotPresent"}}}`,
		b.Spec.Image.Repository, b.Spec.Image.Tag)
	matched := 0
	for i := range raw.Items {
		var e kube.KusoEnvironment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &e); err != nil {
			continue
		}
		if e.Spec.Service != b.Spec.Service {
			continue
		}
		if b.Spec.Branch != "" && e.Spec.Branch != "" && e.Spec.Branch != b.Spec.Branch {
			continue
		}
		if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, e.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("patch env %s: %w", e.Name, err)
		}
		matched++
		p.logger().Info("build promoted", "env", e.Name, "ns", ns, "tag", b.Spec.Image.Tag)
	}
	if matched == 0 {
		p.logger().Warn("build succeeded but no env matched for promotion",
			"service", b.Spec.Service, "branch", b.Spec.Branch, "tag", b.Spec.Image.Tag)
	}
	return nil
}

// asUnstructured is a small helper to build the unstructured shape for
// writing builds in tests.
func asUnstructured(b *kube.KusoBuild) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(b)
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRBuilds.GroupVersion().WithKind("KusoBuild"))
	return u, nil
}

// Used by tests under the same package to build seed objects without
// re-importing kube internals.
var _ = asUnstructured
