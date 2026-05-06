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
	"encoding/json"
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
	"k8s.io/apimachinery/pkg/api/resource"
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
	annPhase         = "kuso.sislelabs.com/build-phase"
	annCompletedAt   = "kuso.sislelabs.com/build-completed-at"
	annStartedAt     = "kuso.sislelabs.com/build-started-at"
	annMessage       = "kuso.sislelabs.com/build-message"
	annSupersededBy  = "kuso.sislelabs.com/superseded-by"
	// detectedEnv carries a JSON-encoded []string of env-var names
	// the build-time scanner surfaced from the source repo. UI uses
	// this to flag "you reference X but it isn't set" at the
	// EnvVarsEditor level. Stored at the build CR (not the service)
	// so re-builds refresh the list and we keep an audit trail.
	annDetectedEnv   = "kuso.sislelabs.com/detected-env"
	annDetectedEnvAt = "kuso.sislelabs.com/detected-env-at"
	// Trigger context — surfaces in BuildSummary so users can see who
	// kicked off a build and why. Helps when a teammate asks "who
	// broke prod at 3pm" — the deployments tab now answers that
	// without git/issue archaeology.
	annTriggerSource = "kuso.sislelabs.com/build-triggered-by"
	annTriggerUser   = "kuso.sislelabs.com/build-triggered-by-user"
	annCommitMessage = "kuso.sislelabs.com/build-commit-message"
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

	// MaxConcurrentBuilds is the fallback cap when no Settings
	// provider is wired (CLI tests, legacy main.go). Production
	// reads the live value from Settings.GetBuildSettings on each
	// admission, so the admin's /settings change takes effect on
	// the next build without a server restart.
	//
	// 0 disables the cap (matches pre-v0.7.17 behaviour). Override
	// per project via the kuso.sislelabs.com/build-max-concurrent
	// annotation on the KusoProject CR.
	MaxConcurrentBuilds int
	// AdmitTimeout is how long Create waits for a build slot to open
	// before returning ErrConflict. Zero defaults to 60s — long
	// enough to absorb a monorepo push storm against a 1-min build,
	// short enough to bound webhook retry latency.
	AdmitTimeout time.Duration

	// Settings reads admin-tunable knobs from the DB. When non-nil,
	// build resource limits + the cluster-wide concurrent cap come
	// from here instead of the static MaxConcurrentBuilds field.
	// Cached for 30s per process so admin saves propagate quickly
	// without burning DB reads on every Create.
	Settings SettingsProvider

	// settingsCache memoizes the last read so per-Create reads
	// don't hit Postgres on every webhook tick.
	settingsMu       sync.Mutex
	settingsCache    *cachedBuildSettings
	settingsCacheTTL time.Duration

	// inFlight dedupes concurrent Create calls keyed on
	// (project, service, sha). When a webhook retries (GitHub does so
	// on 5xx) or two callers race the same build, the second caller
	// waits on the first's outcome instead of creating a duplicate
	// KusoBuild + clone-token Secret. Keys live for the duration of
	// Create only; freed in defer regardless of outcome.
	inFlight sync.Map

	// serviceLocks serializes Create calls for the same (project,
	// service) regardless of SHA. Without this, two simultaneous
	// pushes of different commits both see "no active build" in the
	// active-check, both create CRs without the queued label, both
	// render kaniko Jobs in parallel — defeating the queue. The
	// per-service mutex closes the TOCTOU window between
	// findActiveForService and CreateKusoBuild.
	//
	// We use a map of *sync.Mutex (not sync.Map of mutexes) because
	// we need to lock for the duration of Create — sync.Map's value
	// retrieval is not lock-friendly. The outer mutex protects the
	// map; the per-service mutex protects the active-check + create
	// critical section. Hot-path cost: one map lookup + one Lock()
	// per Create, which is microseconds.
	serviceLocksMu sync.Mutex
	serviceLocks   map[string]*sync.Mutex

	// (Removed in v0.8.10) admitOnce + admitSem — the in-memory
	// semaphore was replaced with a kube-list-based admission gate
	// in admitBuild. Cluster reality is the only source of truth
	// that survives kuso-server restarts and operator-rendered
	// Job pods that bypass Create.

	// Notifier receives build.superseded events when a new build for
	// the same (project, service) cancels an in-flight predecessor.
	// Optional: nil → silent. The Poller has its own Notifier slot
	// for build.{succeeded,failed} events.
	Notifier EventEmitter
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

// SettingsProvider is the read-side of the admin-tunable build
// settings (db.DB satisfies it). Held as an interface so the builds
// package doesn't pull in db, which would force every test to spin
// up Postgres just to construct a Service.
type SettingsProvider interface {
	GetBuildSettings(ctx context.Context) (BuildSettingsView, error)
}

// BuildSettingsView mirrors db.BuildSettings without importing db.
// Carried through this package as the hot-path config used by
// admitBuild + the chart-render path that overrides spec.resources.
type BuildSettingsView struct {
	MaxConcurrent int
	MemoryLimit   string
	MemoryRequest string
	CPULimit      string
	CPURequest    string
}

type cachedBuildSettings struct {
	view    BuildSettingsView
	expires time.Time
}

// loadSettings returns the live build settings, hitting the DB at
// most once per settingsCacheTTL (default 30s). When no Settings
// provider is wired the static MaxConcurrentBuilds + chart defaults
// are returned so the legacy code path keeps working.
func (s *Service) loadSettings(ctx context.Context) BuildSettingsView {
	if s.Settings == nil {
		return BuildSettingsView{MaxConcurrent: s.MaxConcurrentBuilds}
	}
	ttl := s.settingsCacheTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	s.settingsMu.Lock()
	if s.settingsCache != nil && time.Now().Before(s.settingsCache.expires) {
		out := s.settingsCache.view
		s.settingsMu.Unlock()
		return out
	}
	s.settingsMu.Unlock()
	v, err := s.Settings.GetBuildSettings(ctx)
	if err != nil {
		// Fall back to static config; never fail a build because
		// we couldn't reach Postgres. The cache stays empty so the
		// next call retries.
		return BuildSettingsView{MaxConcurrent: s.MaxConcurrentBuilds}
	}
	s.settingsMu.Lock()
	s.settingsCache = &cachedBuildSettings{view: v, expires: time.Now().Add(ttl)}
	s.settingsMu.Unlock()
	return v
}

// inFlightKey produces the dedup key. SHA is the natural primary key
// for a build (image tag derives from it); project + service prevent
// false-positive collisions when two services in different projects
// happen to share a SHA (rare but possible with monorepos).
func inFlightKey(project, service, sha string) string {
	return project + "/" + service + "/" + sha
}

// serviceLockFor returns the per-service mutex, creating it on first
// access. Locks are never freed once created — the entry count is
// bounded by the number of distinct services in the cluster, which
// is the same set we'd already keep in memory for service CRs.
func (s *Service) serviceLockFor(project, service string) *sync.Mutex {
	key := project + "/" + service
	s.serviceLocksMu.Lock()
	defer s.serviceLocksMu.Unlock()
	if s.serviceLocks == nil {
		s.serviceLocks = map[string]*sync.Mutex{}
	}
	mu, ok := s.serviceLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		s.serviceLocks[key] = mu
	}
	return mu
}

// New constructs a builds.Service with a default namespace fallback.
//
// MaxConcurrentBuilds + AdmitTimeout are zero by default (uncapped,
// matches pre-v0.7.17 behaviour). Callers in main.go set them from
// KUSO_BUILD_MAX_CONCURRENT and KUSO_BUILD_ADMIT_TIMEOUT env vars.
func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

// admitBuild gates Create against the cluster-wide concurrent-build
// cap. Returns ErrConflict immediately when at-or-over the cap (i.e.
// don't queue at the admission layer — the queue model already
// handles "wait for capacity" by stamping new CRs as queued). Returns
// nil on admission with a no-op release function (we no longer hold
// a semaphore slot for the duration of CR creation; cluster reality
// is the source of truth).
//
// Why we replaced the in-memory semaphore (v0.8.10):
//
//   - The buffered-channel semaphore lived in one kuso-server replica's
//     memory. After a kuso-server restart, the new pod started with
//     an empty semaphore even though build pods from before the
//     restart were still consuming node resources. On v0.8.9 a
//     redeploy-after-roll triggered 3 simultaneous builds that
//     OOM-thrashed the host because the new replica thought capacity
//     was free.
//   - Operator-rendered Job pods (queued→pending→running by the
//     dispatcher tick) bypassed admitBuild entirely.
//   - With per-service queueing, the only thing left to gate is the
//     promote rate, which the dispatcher already does (one promote
//     per tick per service). The cluster-wide cap is a final
//     safety belt against pathological cross-service storms.
//
// New strategy: count actual running build pods cluster-wide. The
// label app.kubernetes.io/component=kusobuild tags every kaniko Job
// pod the chart renders; one apiserver list per Create is cheap
// (~5ms on a small cluster) and is always correct.
func (s *Service) admitBuild(ctx context.Context, project string) (release func(), err error) {
	cfg := s.loadSettings(ctx)
	if cfg.MaxConcurrent <= 0 {
		return func() {}, nil
	}
	// Per-project lower bound. Cheap CR read; only matters when set.
	projectCap := s.projectBuildCap(ctx, project)
	if projectCap > 0 {
		if active := s.countActiveBuildsForProject(ctx, project); active >= projectCap {
			return nil, fmt.Errorf("%w: project %s at concurrency cap (%d active, cap %d)",
				ErrConflict, project, active, projectCap)
		}
	}
	// Cluster-wide cap based on reality. Counts running build pods
	// across every namespace, which catches builds rendered by the
	// operator from queued CRs, builds left over from a previous
	// kuso-server replica, and builds re-spawned by a Job retry.
	if active := s.countRunningBuildPodsCluster(ctx); active >= cfg.MaxConcurrent {
		return nil, fmt.Errorf("%w: cluster at build concurrency cap (%d active, cap %d)",
			ErrConflict, active, cfg.MaxConcurrent)
	}
	return func() {}, nil
}

// countRunningBuildPodsCluster lists pods labelled as kusobuild
// (either the v0.8.10+ component label or the legacy name label
// every chart version has set since v0.6) across all namespaces and
// returns the count whose phase is Pending or Running. Best-effort:
// kube errors return 0 (admit) — we'd rather risk one extra build
// than wedge the system on a transient apiserver hiccup.
//
// We accept the OR of two label selectors so a roll that mixes pre-
// and post-v0.8.10 operator-rendered Job pods is still counted
// correctly. Without the OR, a Job pod rendered by the old operator
// (no component label) would slip past the cap and we'd over-admit.
func (s *Service) countRunningBuildPodsCluster(ctx context.Context) int {
	if s.Kube == nil {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// One list per selector. The two queries return disjoint sets in
	// the steady state (post-v0.8.10 pods carry both labels); during
	// a roll, dedup by name so we don't double-count.
	seen := map[string]struct{}{}
	count := func(sel string) {
		pods, err := s.Kube.Clientset.CoreV1().Pods("").List(lctx, metav1.ListOptions{
			LabelSelector: sel,
		})
		if err != nil {
			slog.Default().Warn("countRunningBuildPodsCluster", "selector", sel, "err", err)
			return
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodRunning {
				continue
			}
			seen[p.Namespace+"/"+p.Name] = struct{}{}
		}
	}
	count("app.kubernetes.io/component=kusobuild")
	count("app.kubernetes.io/name=kusobuild")
	return len(seen)
}

// projectBuildCap returns the per-project max-concurrent override
// from the KusoProject CR's annotation, or 0 when unset / unparseable.
// Best-effort: any kube error returns 0 (use the global cap).
func (s *Service) projectBuildCap(ctx context.Context, project string) int {
	if s.Kube == nil || project == "" {
		return 0
	}
	gctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p, err := s.Kube.GetKusoProject(gctx, s.Namespace, project)
	if err != nil || p == nil {
		return 0
	}
	v := p.Annotations["kuso.sislelabs.com/build-max-concurrent"]
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// countActiveBuildsForProject returns the number of currently-running
// build pods for a project (not CRs — queued CRs don't render pods
// and don't consume resources). Best-effort: kube errors return 0
// (admit) — we'd rather risk one extra build than wedge.
//
// Accepts either the v0.8.10+ component label or the legacy name
// label so a roll-in-progress doesn't under-count active builds.
func (s *Service) countActiveBuildsForProject(ctx context.Context, project string) int {
	if s.Kube == nil || project == "" {
		return 0
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ns := s.nsFor(lctx, project)
	seen := map[string]struct{}{}
	count := func(sel string) {
		pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			return
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase != corev1.PodPending && p.Status.Phase != corev1.PodRunning {
				continue
			}
			seen[p.Name] = struct{}{}
		}
	}
	count("app.kubernetes.io/component=kusobuild,kuso.sislelabs.com/project=" + project)
	count("app.kubernetes.io/name=kusobuild,kuso.sislelabs.com/project=" + project)
	return len(seen)
}

// findRecentForBranch returns the newest in-flight (running / pending
// / queued) KusoBuild for (project, fqn, branch) created within
// `window`, or nil if none. Used to coalesce rapid synthetic-ref
// redeploys so spam-clicking the Redeploy button doesn't pile up
// duplicate queue entries.
func (s *Service) findRecentForBranch(ctx context.Context, ns, project, fqn, branch string, window time.Duration) (*kube.KusoBuild, error) {
	if s.Kube == nil {
		return nil, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// We want "active OR queued" — anything whose build-state is not
	// "done". A label-selector `!=` only matches keys that exist, so
	// we list everything for the service and filter in code; the
	// label cardinality is bounded (~5-50 builds per service over
	// any short window).
	selector := "kuso.sislelabs.com/project=" + project +
		",kuso.sislelabs.com/service=" + fqn
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(lctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list recent builds: %w", err)
	}
	cutoff := time.Now().Add(-window)
	var best *kube.KusoBuild
	for i := range raw.Items {
		if raw.Items[i].GetLabels()["kuso.sislelabs.com/build-state"] == "done" {
			continue
		}
		var b kube.KusoBuild
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
			continue
		}
		if b.Spec.Branch != branch {
			continue
		}
		// Treat zero creationTimestamp as "just created" (the apiserver
		// would have stamped it but clients/fakes might not). This
		// keeps the coalesce window slightly more permissive — a
		// freshly-created CR with an unstamped TS is still recent.
		if !b.CreationTimestamp.Time.IsZero() && b.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		if best == nil || b.CreationTimestamp.After(best.CreationTimestamp.Time) {
			b := b
			best = &b
		}
	}
	return best, nil
}

// findActiveForService returns the name of an in-flight KusoBuild for
// (project, fqn), or "" if none. "In-flight" = no `build-state` label
// yet (running/pending/queued). Best-effort: kube errors return "" and
// the error is propagated for the caller to log if it cares.
func (s *Service) findActiveForService(ctx context.Context, ns, project, fqn string) (string, error) {
	if s.Kube == nil {
		return "", nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	selector := "kuso.sislelabs.com/project=" + project +
		",kuso.sislelabs.com/service=" + fqn +
		",!kuso.sislelabs.com/build-state"
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(lctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return "", fmt.Errorf("list active builds: %w", err)
	}
	if len(raw.Items) == 0 {
		return "", nil
	}
	return raw.Items[0].GetName(), nil
}

// Cancel marks an in-flight build as cancelled and tears down its
// kaniko Job. The CR itself is preserved (with phase=cancelled +
// build-state=done) so the deployments list still shows it in the
// history rather than a hole. Cancelling a finished build is a no-op
// 400 — the Job's already gone and the phase is fixed.
func (s *Service) Cancel(ctx context.Context, project, service, buildName string) error {
	if buildName == "" {
		return fmt.Errorf("%w: empty build name", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	b, err := s.Kube.GetKusoBuild(ctx, ns, buildName)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: build %s", ErrNotFound, buildName)
	}
	if err != nil {
		return fmt.Errorf("get build: %w", err)
	}
	phase := buildPhase(b)
	if phase == "succeeded" || phase == "failed" || phase == "cancelled" {
		return fmt.Errorf("%w: build %s already in phase %q", ErrInvalid, buildName, phase)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:"cancelled by user"},"labels":{"kuso.sislelabs.com/build-state":"done"}}}`,
		annPhase, annCompletedAt, now, annMessage,
	)
	if _, perr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, buildName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
		return fmt.Errorf("patch build cancelled: %w", perr)
	}
	bg := metav1.DeletePropagationBackground
	if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(ctx, buildName, metav1.DeleteOptions{
		PropagationPolicy: &bg,
	}); jerr != nil && !apierrors.IsNotFound(jerr) {
		// Don't fail the whole Cancel — the CR is already stamped, the
		// poller won't promote it. Worst case the kaniko Job runs for a
		// few more minutes producing an image nothing will use.
		slog.Default().Warn("builds: delete cancelled job", "err", jerr, "build", buildName)
	}
	// Also delete the helm release secrets. Without this, the operator's
	// next reconcile (or a watch event from a Job-deletion ripple) sees
	// "release exists, manifest says Job should exist, Job is missing"
	// and re-renders the kaniko Job. We hit this on 2026-05-05 — a
	// cancelled build kept respawning every 30s until we scaled the
	// operator to 0. The watch selector excludes state=done from
	// future reconciles but the existing release record is the
	// trigger source. Deleting it makes the release a no-op for the
	// operator.
	helmSelector := "owner=helm,name=" + buildName
	secs, lerr := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: helmSelector,
	})
	if lerr == nil {
		for i := range secs.Items {
			name := secs.Items[i].Name
			if derr := s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
				slog.Default().Warn("builds: delete helm release secret",
					"err", derr, "secret", name, "build", buildName)
			}
		}
	}
	// Wait briefly for the build pod to actually disappear so the
	// caller (and the deployments tab refetch) sees a clean state.
	// Best-effort, capped — don't block Cancel for more than 5s if
	// the kubelet is slow to reap.
	awaitPodGone(ctx, s.Kube, ns, buildName, 5*time.Second)
	if s.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, project+"-")
		s.Notifier.Emit(EventEnvelope{
			Type:     "build.cancelled",
			Title:    fmt.Sprintf("⊘ Build cancelled: %s", short),
			Body:     fmt.Sprintf("`%s` cancelled by user", buildName),
			Project:  project,
			Service:  short,
			URL:      buildEventURL(project, short),
			Severity: "info",
		})
	}
	return nil
}

// awaitPodGone polls Pods.List for build pods owned by `buildName`
// until none remain or `timeout` elapses. Best-effort; on timeout we
// proceed without an error since the kubelet will eventually reap.
// The Cancel HTTP path uses this so a UI refetch after cancel sees a
// clean state instead of a "still running" pod row that the kubelet
// deletes 30 seconds later.
func awaitPodGone(ctx context.Context, kc *kube.Client, ns, buildName string, timeout time.Duration) {
	if kc == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := kc.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/instance=" + buildName,
		})
		if err != nil {
			return
		}
		alive := 0
		for i := range pods.Items {
			ph := pods.Items[i].Status.Phase
			if ph == corev1.PodPending || ph == corev1.PodRunning {
				alive++
			}
		}
		if alive == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// supersedePriorBuilds is retained for the cleanup path — it's no
// longer called from Create (v0.8.5: same-service builds queue rather
// than supersede). Other callers may still want the bulk-cancel
// semantics so we leave the helper in place.
//
// Original doc: finds any in-flight KusoBuild for (project, fqn)
// other than newName, stamps it as cancelled, and tears down its kaniko
// Job. Best-effort: kube errors are logged at warn and swallowed —
// the new build still goes ahead. Stamping (not deleting) the prior CR
// keeps build history intact so the canvas + builds list show the
// cancelled outcome instead of a hole.
func (s *Service) supersedePriorBuilds(ctx context.Context, ns, project, fqn, newName string) {
	if s.Kube == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	selector := "kuso.sislelabs.com/project=" + project +
		",kuso.sislelabs.com/service=" + fqn +
		",!kuso.sislelabs.com/build-state"
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).List(lctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		slog.Default().Warn("builds: list active for supersede", "err", err, "project", project, "service", fqn)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range raw.Items {
		name := raw.Items[i].GetName()
		if name == newName {
			continue
		}
		// Patch: phase=cancelled, build-state=done so helm-operator's
		// watch-selector drops the CR + cleanup poller can prune it,
		// superseded-by + a human-readable message for the UI.
		patch := fmt.Sprintf(
			`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}}}`,
			annPhase,
			annCompletedAt, now,
			annSupersededBy, newName,
			annMessage, "superseded by "+newName,
		)
		if _, perr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			Patch(lctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			slog.Default().Warn("builds: patch superseded", "err", perr, "build", name)
			continue
		}
		// Tear down the kaniko Job. Background propagation deletes the
		// pod too. NotFound is fine — the Job hadn't materialised yet
		// (CR was created but operator hadn't reconciled), or the
		// cleanup poller already got it.
		bg := metav1.DeletePropagationBackground
		if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(lctx, name, metav1.DeleteOptions{
			PropagationPolicy: &bg,
		}); jerr != nil && !apierrors.IsNotFound(jerr) {
			slog.Default().Warn("builds: delete superseded job", "err", jerr, "build", name)
		}
		if s.Notifier != nil {
			short := strings.TrimPrefix(fqn, project+"-")
			s.Notifier.Emit(EventEnvelope{
				Type:     "build.superseded",
				Title:    fmt.Sprintf("⊘ Build superseded: %s", short),
				Body:     fmt.Sprintf("`%s` cancelled — replaced by `%s`", name, newName),
				Project:  project,
				Service:  short,
				URL:      buildEventURL(project, short),
				Severity: "info",
			})
		}
	}
}

// nsFor returns the execution namespace for project, defaulting to the
// home Namespace.
func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// ScanNamespaces returns every namespace the build poller / promotion
// flow needs to walk: the home ns plus every distinct spec.namespace
// declared by a KusoProject. Deduped, errors swallowed (always at
// least the home ns is returned).
func (s *Service) ScanNamespaces(ctx context.Context) []string {
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
// Trigger context fields are populated by the handler (user from
// session, webhook from the github controller) — they're not user-
// supplied through the JSON body.
type CreateBuildRequest struct {
	Branch          string `json:"branch,omitempty"`
	Ref             string `json:"ref,omitempty"`
	TriggeredBy     string `json:"-"` // user|webhook|api|system
	TriggeredByUser string `json:"-"`
	CommitMessage   string `json:"-"`
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

	// Admission control: cap cluster-wide simultaneous builds so a
	// monorepo push storm doesn't OOM the single-box node. Blocks up
	// to AdmitTimeout for a slot; ErrConflict on timeout. Released
	// when the build CR is created (the kaniko Job pod is what
	// actually consumes node resources, but we hold the slot until
	// CR creation so the operator's helm render is bounded too).
	release, err := s.admitBuild(ctx, project)
	if err != nil {
		return nil, err
	}
	defer release()

	buildName := buildCRName(project, service, sha)

	// Per-service serialization. Without this, two simultaneous Create
	// calls (different SHAs) both pass findActiveForService with no
	// active build, both decide queued=false, both render Jobs in
	// parallel — defeating the queue model. The mutex covers the
	// active-check through CR creation so the queued/active decision
	// is atomic w.r.t. the etcd state. SHA-level dedup happens above
	// (inFlight); this is the broader serialization needed for
	// different-SHA-same-service.
	svcLock := s.serviceLockFor(project, service)
	svcLock.Lock()
	defer svcLock.Unlock()

	// Coalesce rapid synthetic-ref redeploys. A user spam-clicking
	// Redeploy generates one synthetic ref per click (each carries a
	// unix-ms suffix → unique). Without coalescing, all 10 clicks
	// queue and the user sees a stack of ghost rows. We treat
	// "another build for the same (project, service, branch) created
	// in the last 30s that's still queued/pending/running" as the
	// same-intent retry → return that build, no new CR.
	//
	// Only applies when the caller did NOT specify an explicit ref —
	// webhook-triggered builds carry a real SHA and dedup via the
	// inFlight map above. The 30s window is roughly "two redeploy
	// clicks within human reaction time"; longer windows risk
	// coalescing genuinely-distinct intents.
	if syntheticRef && req.Ref == "" {
		if existing, err := s.findRecentForBranch(ctx, ns, project, fqn, branch, 30*time.Second); err == nil && existing != nil {
			return existing, nil
		}
	}

	// Coolify-aligned queueing: if another build is in flight for this
	// service, mark the new CR as queued instead of refusing it. The
	// build poller's dispatcher (see Poller.dispatchQueued) promotes
	// the queued build once the active one terminates. The chart
	// doesn't render a Job until the build-state=queued label is
	// removed, so a queued CR consumes no node resources.
	queued := false
	if active, err := s.findActiveForService(ctx, ns, project, fqn); err == nil && active != "" && active != buildName {
		queued = true
	}
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
	// Ensure the per-service build cache PVC exists. Kept best-effort:
	// the kusobuild chart's cache mount is gated on .Values.cache.enabled
	// (set true when the PVC name is non-empty). If the PVC create
	// fails for any reason, we proceed without caching — the build is
	// slower but still correct.
	cachePVC := ""
	if !queued && !buildCacheDisabled(proj) {
		// Only ensure the PVC for the build that will actually run.
		// Queued CRs don't render Jobs, and the PVC is only consumed
		// at Job render time anyway. The dispatcher promote path
		// re-runs ensure when it patches in spec.image (so a queued
		// CR promoted later still gets the cache).
		cachePVC = s.ensureBuildCachePVC(ctx, ns, fqn, svcCR, 5)
	}

	labels := map[string]string{
		"kuso.sislelabs.com/project":   project,
		"kuso.sislelabs.com/service":   fqn,
		"kuso.sislelabs.com/build-ref": shortRef(sha),
	}
	annos := map[string]string{}
	if queued {
		// build-state=queued tells the operator's watch selector to
		// skip reconciling this CR — no Job rendered, no resources
		// consumed. Poller.dispatchQueued promotes it (removes the
		// label, stamps phase=pending) when the active build for the
		// same service finishes.
		labels["kuso.sislelabs.com/build-state"] = "queued"
		annos[annPhase] = "queued"
	}
	// Trigger context — captured from the request handler. user/api/
	// webhook/system distinguishes the four valid sources; the user
	// field is the username for "user" source (otherwise the bot/
	// webhook identity). commitMessage comes from the GitHub webhook
	// when available.
	if req.TriggeredBy != "" {
		annos[annTriggerSource] = req.TriggeredBy
	}
	if req.TriggeredByUser != "" {
		annos[annTriggerUser] = req.TriggeredByUser
	}
	if req.CommitMessage != "" {
		annos[annCommitMessage] = req.CommitMessage
	}
	spec := kube.KusoBuildSpec{
		Project:              project,
		Service:              fqn,
		Ref:                  sha,
		Branch:               branch,
		Repo:                 &kube.KusoRepoRef{URL: repoURL, Path: repoPath},
		GithubInstallationID: installationID,
		Strategy:             strategy,
		// Carry strategy-specific configuration from the service
		// spec onto the build CR so the helm chart can render the
		// right command line. Empty pointers leave the chart on
		// its defaults.
		Static:     svcCR.Spec.Static,
		Buildpacks: svcCR.Spec.Buildpacks,
	}
	if !queued {
		spec.Image = &kube.KusoImage{Repository: imageRepo, Tag: ImageTag(sha)}
	}
	if cachePVC != "" {
		spec.Cache = &kube.KusoBuildCache{PVCName: cachePVC}
	}
	// Admin-tunable build resources. When the operator has saved
	// non-default values via the Settings UI we stamp them onto the
	// CR so the chart's `.Values.resources` picks them up; missing
	// fields fall through to the chart's values.yaml defaults.
	settings := s.loadSettings(ctx)
	if settings.MemoryLimit != "" || settings.MemoryRequest != "" ||
		settings.CPULimit != "" || settings.CPURequest != "" {
		spec.Resources = &kube.KusoBuildResources{}
		if settings.MemoryRequest != "" || settings.CPURequest != "" {
			spec.Resources.Requests = &kube.KusoResourceQty{
				CPU: settings.CPURequest, Memory: settings.MemoryRequest,
			}
		}
		if settings.MemoryLimit != "" || settings.CPULimit != "" {
			spec.Resources.Limits = &kube.KusoResourceQty{
				CPU: settings.CPULimit, Memory: settings.MemoryLimit,
			}
		}
	}
	// Note: when queued, spec.Image is intentionally nil. The kusobuild
	// chart's job.yaml gates Job rendering on `.Values.image.tag` —
	// without the image set, no Job is rendered and the CR sits idle
	// until the dispatcher patches in the image (Poller.dispatchQueued).
	// This is what keeps queued CRs cheap (no kaniko pod, no resources)
	// without needing operator-side changes that require an operator-
	// image rebuild to ship.
	// OwnerReferences: cascade-delete the build CR when its parent
	// KusoService is deleted. Without this, deleting a service leaves
	// orphan KusoBuild records (and any queued builds whose Job never
	// rendered) accumulating in the namespace forever. The
	// kusoservice CRD's helm release stays a peer of the build, so
	// the operator's own uninstall finalizer doesn't conflict.
	owners := []metav1.OwnerReference{}
	if svcCR != nil && svcCR.UID != "" {
		owners = append(owners, metav1.OwnerReference{
			APIVersion: "application.kuso.sislelabs.com/v1alpha1",
			Kind:       "KusoService",
			Name:       svcCR.Name,
			UID:        svcCR.UID,
			// blockOwnerDeletion=false: don't block service delete on
			// build cleanup. controller=false: build CR is not
			// reconciled because of its owner relationship; helm-
			// operator picks it up on its own watch.
		})
	}
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildName,
			Labels:          labels,
			Annotations:     annos,
			OwnerReferences: owners,
		},
		Spec: spec,
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

// ensureBuildCachePVC upserts a PVC named <fqn>-build-cache in the
// project's namespace. Each build mounts it at /cache and uses
// /cache/nix as the persistent nix store + /cache/deps as the
// per-language dep cache root. Owned by the KusoService so it
// cascade-deletes when the service is deleted.
//
// Idempotent: returns nil if the PVC already exists. Size is fixed
// at first creation; resizing later requires either kube's volume-
// resize feature flag (which most installs don't have) or manual PV
// recreation.
//
// Best-effort: on any kube error we return nil and log a warn —
// the build still runs without the cache (just slower). The cache
// is a perf optimisation, not a correctness requirement, so we
// never fail the build for a PVC create issue.
func (s *Service) ensureBuildCachePVC(ctx context.Context, ns, fqn string, svcCR *kube.KusoService, sizeGi int) string {
	if s.Kube == nil {
		return ""
	}
	pvcName := fqn + "-build-cache"
	if sizeGi <= 0 {
		sizeGi = 5
	}
	// OwnerReference back to the KusoService — cascade-delete when
	// the user removes the service.
	owners := []metav1.OwnerReference{}
	if svcCR != nil && svcCR.UID != "" {
		owners = append(owners, metav1.OwnerReference{
			APIVersion: "application.kuso.sislelabs.com/v1alpha1",
			Kind:       "KusoService",
			Name:       svcCR.Name,
			UID:        svcCR.UID,
		})
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pvcName,
			Namespace:       ns,
			OwnerReferences: owners,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
				"kuso.sislelabs.com/service":   fqn,
				"kuso.sislelabs.com/role":      "build-cache",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", sizeGi)),
				},
			},
		},
	}
	_, err := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Create(ctx, pvc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return pvcName
	}
	if err != nil {
		slog.Default().Warn("ensureBuildCachePVC", "ns", ns, "name", pvcName, "err", err)
		return ""
	}
	return pvcName
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

// buildEventURL composes the dashboard path that build.* events
// link to. Mirrors notify.serviceURL but kept local because the
// builds package can't import notify (would create a layering
// inversion: domain code → infra). Empty when project or service
// is missing — the popover renders a non-clickable row in that
// case.
func buildEventURL(project, service string) string {
	if project == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("/projects/%s?service=%s", project, service)
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

// LogArchiver persists the last N lines of a build pod's logs at
// terminal-phase transition so the deployments-tab can show them
// after the kaniko Job pod has been GC'd by its TTL. Implemented by
// db.DB; kept as a small interface to avoid pulling the db package
// into builds (which would force every test to spin up SQLite).
type LogArchiver interface {
	SaveBuildLog(ctx context.Context, buildName, project, service, phase, logs string) error
}

type Poller struct {
	Svc      *Service
	Interval time.Duration
	Logger   *slog.Logger
	// Notifier receives build.{started,succeeded,failed} events.
	// Optional: nil → no notifications.
	Notifier EventEmitter
	// LogArchive snapshots the kaniko pod's tail logs into SQLite at
	// terminal phase. Optional: nil → no archive (pod logs vanish on
	// TTL as before).
	LogArchive LogArchiver
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
	for _, ns := range p.Svc.ScanNamespaces(ctx) {
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
		// Queue dispatcher: promote the oldest queued build per service
		// when no active (running/pending) build exists for it. Runs
		// after the activeBuilds sweep so a build that finished THIS
		// tick has its queued sibling promoted on the next one — keeps
		// the state machine simple at the cost of one tick of latency.
		p.dispatchQueued(ctx, ns)
	}
	return nil
}

// dispatchQueued promotes queued builds to running when their service
// has no active build. One queued build per service per tick to avoid
// stampedes on a small cluster (the operator's renderer is the
// bottleneck; bulk-promoting 10 queued builds at once would just
// re-create the OOM-thrash scenario from v0.7.x). Best-effort: kube
// errors are warn-logged and the next tick retries.
func (p *Poller) dispatchQueued(ctx context.Context, ns string) {
	raw, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		List(ctx, metav1.ListOptions{LabelSelector: "kuso.sislelabs.com/build-state=queued"})
	if err != nil {
		p.Logger.Warn("build poller queue list", "ns", ns, "err", err)
		return
	}
	if len(raw.Items) == 0 {
		return
	}
	// Group queued builds by service + sort each group oldest-first so
	// the FIFO order is preserved (the user expects "first redeploy
	// click runs first"). creationTimestamp ties break on name lex.
	byService := map[string][]*kube.KusoBuild{}
	for i := range raw.Items {
		var b kube.KusoBuild
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Items[i].Object, &b); err != nil {
			continue
		}
		byService[b.Spec.Service] = append(byService[b.Spec.Service], &b)
	}
	for fqn, list := range byService {
		// Active check per service. If anything's running, skip the
		// whole queue for this service this tick.
		project := list[0].Spec.Project
		active, err := p.Svc.findActiveForService(ctx, ns, project, fqn)
		if err != nil {
			p.Logger.Warn("build poller queue active check", "ns", ns, "service", fqn, "err", err)
			continue
		}
		if active != "" {
			continue
		}
		// Oldest first.
		sort.SliceStable(list, func(i, j int) bool {
			ti := list[i].CreationTimestamp
			tj := list[j].CreationTimestamp
			if ti.Equal(&tj) {
				return list[i].Name < list[j].Name
			}
			return ti.Before(&tj)
		})
		next := list[0]
		// Promote: remove the queued label (operator's selector now
		// matches it), stamp phase=pending, AND patch in spec.image
		// (the chart's render gate). The image is computed the same
		// way Create computes it for an immediate build — repo +
		// service short name + the Ref's image tag.
		shortName := strings.TrimPrefix(fqn, project+"-")
		imageRepo := fmt.Sprintf("%s/%s/%s", RegistryHost, project, shortName)
		// Ensure the build cache PVC exists at promote time too — if
		// the queued CR was created during a window where the PVC
		// briefly didn't exist (or the Create-time ensure failed),
		// catch it here so the promoted build still gets the cache.
		// Best-effort; empty pvcName means the chart skips the cache
		// mount and runs cold.
		var svcCR *kube.KusoService
		if s, err := p.Svc.Kube.GetKusoService(ctx, ns, fqn); err == nil {
			svcCR = s
		}
		var projCR *kube.KusoProject
		if pp, err := p.Svc.Kube.GetKusoProject(ctx, p.Svc.Namespace, project); err == nil {
			projCR = pp
		}
		cachePVC := ""
		if !buildCacheDisabled(projCR) {
			cachePVC = p.Svc.ensureBuildCachePVC(ctx, ns, fqn, svcCR, 5)
		}
		cachePatch := ""
		if cachePVC != "" {
			cachePatch = fmt.Sprintf(`,"cache":{"pvcName":%q}`, cachePVC)
		}
		patch := fmt.Sprintf(
			`{"metadata":{"labels":{"kuso.sislelabs.com/build-state":null},"annotations":{%q:"pending"}},"spec":{"image":{"repository":%q,"tag":%q}%s}}`,
			annPhase, imageRepo, ImageTag(next.Spec.Ref), cachePatch,
		)
		if _, perr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			Patch(ctx, next.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			p.Logger.Warn("build poller promote queued", "build", next.Name, "ns", ns, "err", perr)
			continue
		}
		p.Logger.Info("build poller promoted queued build", "build", next.Name, "service", fqn)
	}
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
// archiveLogs snapshots the last 200 lines of the kaniko pod's logs
// into the BuildLog table. Best-effort: kube errors are logged at
// warn and swallowed — the build's terminal phase is the load-bearing
// patch, log archiving is a UX nice-to-have. Selects the pod by the
// helm release-instance label the chart sets to the build CR name;
// concatenates all pods (init container + main container split).
func (p *Poller) archiveLogs(ctx context.Context, ns string, b *kube.KusoBuild, phase string) {
	if p.LogArchive == nil || p.Svc == nil || p.Svc.Kube == nil {
		return
	}
	const tailLines = 200
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pods, err := p.Svc.Kube.Clientset.CoreV1().Pods(ns).List(lctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + b.Name,
	})
	if err != nil {
		slog.Default().Warn("builds: archive list pods", "err", err, "build", b.Name)
		return
	}
	var combined strings.Builder
	tail := int64(tailLines)
	for i := range pods.Items {
		pod := &pods.Items[i]
		// Each pod has a clone init container + a main kaniko container.
		// We pull from each container individually so failures in init
		// (git clone bad ref, missing token) survive too — the operator
		// would otherwise lose them.
		for _, c := range append(append([]string{}, containerNames(pod.Spec.InitContainers)...), containerNames(pod.Spec.Containers)...) {
			req := p.Svc.Kube.Clientset.CoreV1().Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
				Container: c,
				TailLines: &tail,
			})
			stream, err := req.Stream(lctx)
			if err != nil {
				continue
			}
			data := make([]byte, 0, 8192)
			buf := make([]byte, 4096)
			for {
				n, rerr := stream.Read(buf)
				if n > 0 {
					data = append(data, buf[:n]...)
				}
				if rerr != nil {
					break
				}
			}
			stream.Close()
			if len(data) == 0 {
				continue
			}
			if combined.Len() > 0 {
				combined.WriteString("\n")
			}
			combined.WriteString("--- container: ")
			combined.WriteString(c)
			combined.WriteString(" ---\n")
			combined.Write(data)
		}
	}
	logs := combined.String()
	// Extract detected-env BEFORE the tail truncation: the env-detect
	// init container emits its sentinel block early in the build, so
	// it'd get cut off when a long kaniko stage drowns the tail.
	if detected := parseDetectedEnv(logs); detected != nil {
		// Stash on the build CR's status. Best-effort; a failure here
		// just means the UI won't see the suggestion list — the build
		// itself is unaffected.
		if err := p.persistDetectedEnv(lctx, ns, b, detected); err != nil {
			slog.Default().Warn("builds: persist detectedEnv", "err", err, "build", b.Name)
		}
	}
	// Last-N-line cap. The kaniko build can spit 5k+ lines in a noisy
	// build (apt-get progress, nix derivations); we want the tail
	// because that's where the failure usually is.
	if lines := strings.Split(logs, "\n"); len(lines) > tailLines {
		logs = strings.Join(lines[len(lines)-tailLines:], "\n")
	}
	if err := p.LogArchive.SaveBuildLog(lctx, b.Name, b.Spec.Project,
		strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-"), phase, logs); err != nil {
		slog.Default().Warn("builds: archive save", "err", err, "build", b.Name)
	}
}

// detectedEnvRe matches the env-detect init container's sentinel-fenced
// JSON block. The init writes the block in one go so we don't need to
// reassemble across ANSI/timestamp prefixes.
var detectedEnvRe = regexp.MustCompile(`(?s)KUSO_ENV_DETECT_BEGIN\s*\n(\[.*?\])\s*\nKUSO_ENV_DETECT_END`)

// parseDetectedEnv extracts the var-name list from build logs. Returns
// nil when the sentinel isn't present (e.g. older build pod, build
// failed before env-detect ran). The list is JSON-decoded so a malformed
// block is just dropped rather than corrupting the build status.
func parseDetectedEnv(logs string) []string {
	m := detectedEnvRe.FindStringSubmatch(logs)
	if len(m) < 2 {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(m[1]), &names); err != nil {
		return nil
	}
	// Empty list is meaningful (= scan ran, found nothing); preserve it.
	if names == nil {
		names = []string{}
	}
	return names
}

// persistDetectedEnv stamps the parsed list onto KusoBuild's
// annotations. Annotations (not .status) because helm-operator owns
// the status subresource on these CRs and would clobber a write race.
// Same pattern used by the build-phase tracker (annPhase). The chart
// doesn't read this — it's purely consumed by the API surface for the
// EnvVarsEditor's "missing vars" suggestion.
//
// Encoding: JSON-serialised array under
// `kuso.sislelabs.com/detected-env`, plus a sibling timestamp at
// `kuso.sislelabs.com/detected-env-at` so the UI can show "detected
// at <build trigger time>".
func (p *Poller) persistDetectedEnv(ctx context.Context, ns string, b *kube.KusoBuild, names []string) error {
	if p.Svc == nil || p.Svc.Kube == nil {
		return nil
	}
	if names == nil {
		names = []string{}
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return err
	}
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		annDetectedEnv, string(encoded),
		annDetectedEnvAt, time.Now().UTC().Format(time.RFC3339),
	))
	_, err = p.Svc.Kube.Dynamic.
		Resource(kube.GVRBuilds).
		Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// containerNames extracts pod container names. Wrapped because the
// init+main split needs concatenating and a tiny helper makes
// archiveLogs readable.
func containerNames(cs []corev1.Container) []string {
	out := make([]string, 0, len(cs))
	for i := range cs {
		out = append(out, cs[i].Name)
	}
	return out
}

func (p *Poller) markSucceeded(ctx context.Context, ns string, b *kube.KusoBuild) error {
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"succeeded",%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}}}`,
		annPhase, annCompletedAt, time.Now().UTC().Format(time.RFC3339),
	)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch build status: %w", err)
	}
	// Snapshot the pod logs BEFORE the kaniko Job's TTL reaper can
	// delete them. 1h is the chart's default ttlSecondsAfterFinished,
	// but a slow tick interleaved with a slow apiserver could miss
	// the window — taking the snapshot synchronously here costs ~1s
	// and removes that race.
	p.archiveLogs(ctx, ns, b, "succeeded")
	if p.Notifier != nil {
		ref := b.Spec.Ref
		if len(ref) > 12 {
			ref = ref[:12]
		}
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		p.Notifier.Emit(EventEnvelope{
			Type:     "build.succeeded",
			Title:    fmt.Sprintf("✓ Build succeeded: %s", short),
			Body:     fmt.Sprintf("ref `%s` on `%s`", ref, b.Spec.Branch),
			Project:  b.Spec.Project,
			Service:  short,
			URL:      buildEventURL(b.Spec.Project, short),
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
	p.archiveLogs(ctx, ns, b, "failed")
	if p.Notifier != nil {
		ref := b.Spec.Ref
		if len(ref) > 12 {
			ref = ref[:12]
		}
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		p.Notifier.Emit(EventEnvelope{
			Type:     "build.failed",
			Title:    fmt.Sprintf("✗ Build failed: %s", short),
			Body:     msg,
			Project:  b.Spec.Project,
			Service:  short,
			URL:      buildEventURL(b.Spec.Project, short),
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
	// Stamp started-at the first time we see the kaniko Job pod active.
	// Without this the build-summary endpoint has no startedAt for the
	// running build, the deployments panel can't compute a live duration,
	// and the row shows just "12s ago" with no elapsed timer. Set it
	// once on the queued→running transition; markSucceeded/markFailed
	// don't touch it so finished builds keep their started-at intact.
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"running",%q:%q}}}`,
		annPhase, annStartedAt, time.Now().UTC().Format(time.RFC3339),
	))
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
