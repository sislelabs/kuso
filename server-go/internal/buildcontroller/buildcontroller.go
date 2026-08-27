// Package buildcontroller renders KusoBuild CRs into kube Jobs
// directly, replacing the helm-operator-driven path that used the
// operator/helm-charts/kusobuild chart.
//
// Background: every CR write that flowed through the helm-operator
// paid the 3-minute reconcile + per-CR helm-render tax. For builds
// — transient, fast-cycling, often arriving in bursts during a
// Coolify import or a monorepo push — that was the wrong tool.
// Three different patches accumulated to paper over the seam: the
// chart's top-level done=true no-op gate, the Cancel-time tag-
// blanking that defangs the chart values, and the helm-release
// secret-delete that the operator otherwise resurrects from.
//
// This controller owns Job creation directly. It watches KusoBuild
// CRs via the existing dynamic informer (no controller-runtime
// dep), and when a CR appears that isn't done, it renders a Job
// straight from the CR spec and applies it. Reconcile is O(1) per
// event (no helm template render), so bursts of 50-500 builds from
// a Coolify import commit no longer queue behind the operator's
// per-kind worker pool.
//
// What stays the same:
//   - KusoBuild CRD shape (unchanged on disk).
//   - Build poller (internal/builds.Poller) continues to observe
//     Job state and patch the CR's status annotations.
//   - Cancel path (builds.Service.Cancel) still stamps done=true
//     and deletes the Job. The reaper still sweeps helm-release
//     secrets for any stragglers from the pre-controller path.
//   - kuso-buildkitd Deployment + buildkit Service stay in
//     deploy/buildkitd.yaml; this controller just renders the
//     client Job pointing at it.
//
// What's different:
//   - operator/watches.yaml no longer lists KusoBuild. The
//     operator does not reconcile build CRs.
//   - operator/helm-charts/kusobuild/ remains in the tree as a
//     compatibility stub for older clusters that haven't rolled
//     forward yet, but new installs do not deploy it.
//   - There is no helm-release Secret per build, so the reaper's
//     work shrinks to "clean up any pre-existing secrets from the
//     pre-controller era" (idempotent NotFound → no-op).
package buildcontroller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// Defaults applied when the CR's spec.* values are empty. The chart
// used to carry these in values.yaml; centralising them here keeps
// the controller self-contained.
const (
	defaultEnvDetectImage     = "ghcr.io/sislelabs/kuso-env-detect"
	defaultEnvDetectTag       = "v1"
	defaultNixpacksImage      = "ghcr.io/sislelabs/kuso-nixpacks"
	defaultNixpacksVersion    = "1.41.0"
	defaultBuildpacksImage    = "buildpacksio/lifecycle:0.20.5"
	defaultBuildpacksBuilder  = "paketobuildpacks/builder-jammy-base:latest"
	defaultStaticBuilderImage = "node:20-alpine"
	defaultStaticRuntimeImage = "nginx:1.27-alpine"
	defaultBuildkitImage      = "moby/buildkit:v0.16.0"
	defaultCloneImage         = "alpine/git:2.45.2"
	defaultCacheInitImage     = "alpine:3.20"
	defaultBuildkitHost       = "tcp://kuso-buildkitd.kuso.svc.cluster.local:1234"

	defaultCPURequest   = "200m"
	defaultMemRequest   = "512Mi"
	defaultCPULimit     = "1500m"
	defaultMemLimit     = "2Gi"
	jobTTLSecondsAfter  = int32(3600)
	jobActiveBudgetMins = int32(60) // ActiveDeadlineSeconds = 1h ceiling
	jobBackoffLimit     = int32(0)

	// Passive-retry policy for ensureJob failures (resilience W5).
	// Before this existed, a failed ensureJob only dropped the dedup
	// key and waited for the NEXT informer event — which never comes
	// for a freshly-created CR nobody is patching (the build poller
	// only stamps annotations once a Job exists), so a transient
	// apiserver blip at exactly the wrong moment stranded the build
	// as "pending" until the informer's 10-minute resync.
	//
	// Exponential ladder: 5s → 10s → 20s → 40s → 80s → 160s → 5m →
	// 5m (capped), 8 retries total ≈ 15 minutes of patience, then a
	// loud give-up that stamps the CR failed so it surfaces in the
	// Deployments tab instead of dangling forever.
	retryBaseDelay   = 5 * time.Second
	retryMaxDelay    = 5 * time.Minute
	retryMaxAttempts = 8
)

// Service is the controller entry point. Held on the server-go Deps
// (alongside the build poller + reaper). Start() installs the
// informer handler exactly once per process; per-event work is gated
// on LeaderActive so a non-leader replica observes events but does
// not act on them.
//
// LIFETIME: Start must be called exactly once at boot, NOT once per
// leader-election cycle. The previous shape (one Start per leader
// acquire) accumulated event handlers on the shared informer —
// after N re-elections every CR event fired N reconcile closures,
// each with its own `running` dedup map that couldn't see the
// others. sync.Once defends against repeat-call programming errors;
// the canonical wiring is "boot calls Start once, leader controls
// LeaderActive."
type Service struct {
	Kube      *kube.Client
	Cache     *kube.Cache
	Namespace string // home namespace (only used for cross-ns logging)
	Logger    *slog.Logger
	// LeaderActive gates per-event work. nil = always-active (safe
	// for single-replica deploys where leader election is bypassed);
	// non-nil = only reconcile while this atomic reads true.
	LeaderActive *atomic.Bool

	// running tracks CRs we've already kicked off a reconcile for, to
	// dedup against the informer's update floods (the build poller
	// patches annotations every few seconds while a Job is active,
	// and each patch fires an Update event we'd otherwise
	// re-reconcile).
	mu      sync.Mutex
	running map[string]struct{}

	// retries tracks per-CR backoff state for failed ensureJob calls
	// (guarded by mu, same as running). Entry lifecycle: created on
	// the first ensureJob failure, timer re-armed with exponential
	// backoff on each subsequent failure, cleared on success, CR
	// delete, terminal CR, leadership loss, or give-up. The retry
	// path re-enters reconcile(), so it shares the running-key CAS
	// with the informer-event path — a retry firing concurrently with
	// an informer event can never double-create the Job.
	retries map[string]*retryState

	// retryBase / retryMax override the retry ladder in tests.
	// Zero values mean retryBaseDelay / retryMaxAttempts.
	retryBase time.Duration
	retryMax  int

	// startOnce makes Start idempotent. The whole struct is intended
	// to live for the process lifetime; accidental double-Start (e.g.
	// from a future refactor that wires it inside the leader block
	// again) becomes a no-op rather than handler duplication.
	startOnce sync.Once
}

// Start installs the AddEventHandler on the KusoBuild informer.
// Non-blocking — the informer's worker runs the handler. Idempotent:
// safe to call repeatedly, but ONLY the first call wires the handler.
// Call exactly once at boot.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.Cache == nil || s.Kube == nil {
		return
	}
	s.startOnce.Do(func() { s.installHandler(ctx) })
}

func (s *Service) installHandler(ctx context.Context) {
	if s.running == nil {
		s.running = make(map[string]struct{})
	}
	inf := s.Cache.CRDInformer(kube.GVRBuilds)
	if inf == nil {
		if s.Logger != nil {
			s.Logger.Warn("buildcontroller: no informer for KusoBuild — skipped")
		}
		return
	}
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { s.maybeReconcile(ctx, obj, "add") },
		UpdateFunc: func(_, newObj any) { s.maybeReconcile(ctx, newObj, "update") },
		DeleteFunc: s.handleDelete,
	})
	if err != nil && s.Logger != nil {
		s.Logger.Warn("buildcontroller: AddEventHandler", "err", err)
	}
	if s.Logger != nil {
		s.Logger.Info("buildcontroller: started — rendering KusoBuild → Job in-process")
	}
}

// handleDelete drops the deleted CR's key from the running dedup set so a
// same-name rebuild is reconciled instead of deduped forever.
//
// It MUST unwrap cache.DeletedFinalStateUnknown. When the informer's watch
// connection drops and relists, any delete that happened during the gap is
// delivered not as the object itself but as a DeletedFinalStateUnknown
// tombstone wrapping the last-known object. The old handler only type-
// asserted *unstructured.Unstructured, so a missed delete never cleared the
// running key — and because reconcile no-ops when the key is present, a
// build CR of the same name (a retriggered build after the first was
// deleted) stayed deduped and its Job was never rendered. Unwrapping the
// tombstone (standard client-go pattern) recovers the embedded object and
// clears the key.
func (s *Service) handleDelete(obj any) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	key := u.GetNamespace() + "/" + u.GetName()
	s.mu.Lock()
	delete(s.running, key)
	s.mu.Unlock()
	// A deleted CR has nothing left to retry — stop any pending
	// backoff timer so it doesn't fire against a ghost.
	s.clearRetry(key)
}

// maybeReconcile is the leader-gated dispatch step. Non-leaders see
// every event but do nothing, so the lease holder is the sole writer.
func (s *Service) maybeReconcile(ctx context.Context, obj any, source string) {
	if s.LeaderActive != nil && !s.LeaderActive.Load() {
		return
	}
	s.reconcile(ctx, obj, source)
}

// ResyncActive reconciles every not-done KusoBuild currently in the
// informer cache. Call it right after this replica becomes leader: during
// a lease transfer (~15s), an Add event fires on BOTH the outgoing and
// incoming leader and both skip it (the old leader is losing the gate, the
// new one hasn't taken it yet), so a build created in that window would
// otherwise sit unreconciled until the informer's 10-minute resync. This
// one-shot sweep — O(active builds) — closes the gap deterministically.
// reconcile() itself skips terminal CRs and is idempotent (it no-ops when
// the Job already exists), so re-touching in-flight builds is safe.
func (s *Service) ResyncActive(ctx context.Context) {
	if s == nil || s.Cache == nil {
		return
	}
	items, ok := s.Cache.ListFromCache(kube.GVRBuilds, "", labels.Everything())
	if !ok {
		if s.Logger != nil {
			s.Logger.Warn("buildcontroller: resync skipped — build informer not synced")
		}
		return
	}
	n := 0
	for _, u := range items {
		done, _, _ := unstructured.NestedBool(u.Object, "spec", "done")
		if done {
			continue
		}
		s.reconcile(ctx, u, "leader-resync")
		n++
	}
	if s.Logger != nil {
		s.Logger.Info("buildcontroller: leader resync swept in-flight builds", "count", n)
	}
}

// reconcile is the per-event entry point. Decodes the unstructured
// into our typed KusoBuild and dispatches to ensureJob.
func (s *Service) reconcile(ctx context.Context, obj any, source string) {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return
	}
	b, err := decodeBuild(u)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Warn("buildcontroller: decode", "err", err,
				"ns", u.GetNamespace(), "name", u.GetName())
		}
		return
	}
	// Skip terminal CRs — Cancel + markSucceeded + markFailed all
	// stamp spec.done=true. No Job should exist for these; if one
	// does, the existing Cancel path or the reaper handles cleanup.
	// Any pending retry is moot for a terminal CR — drop it so the
	// retries map doesn't accumulate dead entries.
	if b.Spec.Done {
		s.clearRetry(u.GetNamespace() + "/" + u.GetName())
		return
	}
	// Skip promotion-held builds (kuso.sislelabs.com/promote-hold,
	// stamped by builds/promotion_group.go): their Job COMPLETED
	// successfully — they're non-terminal only because the atomic
	// same-repo gate is waiting on sibling builds. Re-ensuring here
	// would recreate the TTL-reaped Job on the next leadership
	// handover's ResyncActive and re-run the entire kaniko build.
	if b.Annotations["kuso.sislelabs.com/promote-hold"] != "" {
		return
	}
	// Belt-and-braces validity check, mirroring the chart's top-level
	// guard. A partially-written CR (missing image.repository or
	// repo.url) can't produce a usable Job; skip silently — the
	// kuso-server Create path validates these before stamping the
	// CR, so seeing one here means an external apply.
	if b.Spec.Image == nil || b.Spec.Image.Repository == "" || b.Spec.Image.Tag == "" {
		return
	}
	if b.Spec.Repo == nil || b.Spec.Repo.URL == "" {
		return
	}

	// Refuse to reconcile CRs outside kuso-managed namespaces. An
	// admin (or compromised kuso-server with cluster-wide write
	// perms) could apply a KusoBuild in `kube-system` — which carries
	// no kuso PSA labels — and the controller would happily schedule
	// a root-running build pod there, outside the baseline-enforced
	// boundary. The managed-by=kuso label on the
	// namespace is the same gate the BuildKit NetworkPolicy uses;
	// we're enforcing it here at the controller level too so the
	// namespace serves as a single coherent trust boundary.
	{
		ns := u.GetNamespace()
		nsCtx, nsCancel := context.WithTimeout(ctx, 3*time.Second)
		managed, mErr := s.Kube.IsManagedNamespace(nsCtx, ns)
		nsCancel()
		if mErr != nil {
			if s.Logger != nil {
				s.Logger.Warn("buildcontroller: namespace check failed; skipping",
					"err", mErr, "ns", ns, "build", u.GetName())
			}
			return
		}
		if !managed {
			if s.Logger != nil {
				s.Logger.Warn("buildcontroller: refusing to reconcile KusoBuild in unmanaged namespace",
					"ns", ns, "build", u.GetName(),
					"hint", "stamp app.kubernetes.io/managed-by=kuso on the namespace if this is intentional")
			}
			return
		}
	}

	key := u.GetNamespace() + "/" + u.GetName()
	s.mu.Lock()
	if _, already := s.running[key]; already {
		// Another reconcile holds the key (its ensureJob is in flight).
		// If a retry timer fired into this race, retryFire already set
		// st.timer=nil — and if we just returned, neither scheduleRetry
		// nor clearRetry would ever run for THIS fire, leaving a
		// timer-less ledger entry whose stale attempts count shortens a
		// future unrelated failure's backoff ladder. Re-arm the timer
		// instead of dropping the fire: the holder's outcome still wins
		// (success → clearRetry stops this timer and deletes the entry;
		// failure → scheduleRetry sees a live timer and keeps it).
		// Bounded: attempts is NOT incremented — losing a CAS race is
		// not an ensureJob failure — and the holder resolves within its
		// 30s reconcile budget, so the re-armed fire finds the key free.
		if st := s.retries[key]; st != nil && st.timer == nil {
			st.timer = time.AfterFunc(s.backoffFor(st.attempts), func() { s.retryFire(ctx, key) })
		}
		s.mu.Unlock()
		return
	}
	s.running[key] = struct{}{}
	s.mu.Unlock()

	reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.ensureJob(reconcileCtx, u, b); err != nil {
		// Drop from `running` so a retry (or the next informer
		// event) can re-enter, then arm the backoff ladder. Note
		// scheduleRetry gets the PARENT ctx, not reconcileCtx —
		// the deferred cancel above would kill the timer's context
		// the moment this function returns.
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
		if s.Logger != nil {
			s.Logger.Warn("buildcontroller: ensure job",
				"err", err, "build", u.GetName(), "ns", u.GetNamespace(), "source", source)
		}
		s.scheduleRetry(ctx, key, u)
		return
	}
	// Success clears any backoff state left by earlier failures —
	// including a pending timer armed by a previous attempt (the
	// informer-event path can succeed while a retry is still queued).
	s.clearRetry(key)
	if s.Logger != nil {
		s.Logger.Info("buildcontroller: ensured job",
			"build", u.GetName(), "ns", u.GetNamespace(), "source", source)
	}
}

// retryState is the per-CR backoff ledger. attempts counts scheduled
// retries (not the initial informer-event attempt); timer is non-nil
// exactly while a retry is queued; obj is the last-seen CR, used as a
// fallback when the informer cache is unavailable at fire time.
type retryState struct {
	attempts int
	timer    *time.Timer
	obj      *unstructured.Unstructured
}

// scheduleRetry arms (or declines to arm) a backoff retry after an
// ensureJob failure. Double-enqueue safe: if a retry is already
// pending for the key, the existing timer is kept — a concurrent
// informer-event failure must not reset the ladder or stack timers.
// Past retryMaxAttempts it gives up loudly via giveUp.
func (s *Service) scheduleRetry(ctx context.Context, key string, u *unstructured.Unstructured) {
	maxAttempts := s.retryMax
	if maxAttempts <= 0 {
		maxAttempts = retryMaxAttempts
	}
	s.mu.Lock()
	if s.retries == nil {
		s.retries = make(map[string]*retryState)
	}
	st := s.retries[key]
	if st == nil {
		st = &retryState{}
		s.retries[key] = st
	}
	if st.timer != nil {
		// A retry is already queued; let it run its course.
		s.mu.Unlock()
		return
	}
	st.attempts++
	st.obj = u
	attempt := st.attempts
	if attempt > maxAttempts {
		delete(s.retries, key)
		s.mu.Unlock()
		s.giveUp(ctx, u, attempt-1)
		return
	}
	delay := s.backoffFor(attempt)
	st.timer = time.AfterFunc(delay, func() { s.retryFire(ctx, key) })
	s.mu.Unlock()
	if s.Logger != nil {
		s.Logger.Info("buildcontroller: ensure job failed — retry scheduled",
			"build", u.GetName(), "ns", u.GetNamespace(),
			"attempt", attempt, "max", maxAttempts, "delay", delay)
	}
}

// backoffFor returns the delay before the given (1-based) retry
// attempt: base·2^(attempt-1), capped at retryMaxDelay.
func (s *Service) backoffFor(attempt int) time.Duration {
	base := s.retryBase
	if base <= 0 {
		base = retryBaseDelay
	}
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d
}

// retryFire runs when a backoff timer expires. It re-checks the leader
// gate (the lease may have moved while we waited — the new leader's
// ResyncActive owns in-flight builds then), refreshes the CR from the
// informer cache when available (the build may have been cancelled or
// deleted in the meantime), and re-enters reconcile, which re-runs the
// same terminal/validity guards and the running-key CAS as any other
// event source.
func (s *Service) retryFire(ctx context.Context, key string) {
	s.mu.Lock()
	st := s.retries[key]
	if st == nil {
		s.mu.Unlock()
		return
	}
	st.timer = nil
	obj := st.obj
	s.mu.Unlock()
	if obj == nil || ctx.Err() != nil {
		return
	}
	if s.LeaderActive != nil && !s.LeaderActive.Load() {
		s.clearRetry(key)
		return
	}
	if s.Cache != nil {
		if items, ok := s.Cache.ListFromCache(kube.GVRBuilds, obj.GetNamespace(), labels.Everything()); ok {
			var fresh *unstructured.Unstructured
			for _, it := range items {
				if it.GetName() == obj.GetName() {
					fresh = it
					break
				}
			}
			if fresh == nil {
				// CR deleted while we waited — nothing to build.
				s.clearRetry(key)
				return
			}
			obj = fresh
		}
	}
	s.reconcile(ctx, obj, "retry")
}

// clearRetry drops the backoff state for a key, stopping any pending
// timer. Called on ensureJob success, CR delete, terminal CR, and
// leadership loss at fire time.
func (s *Service) clearRetry(key string) {
	s.mu.Lock()
	if st := s.retries[key]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(s.retries, key)
	}
	s.mu.Unlock()
}

// giveUp is the loud end of the retry ladder. The slog.Error is the
// primary signal; the merge-patch is best-effort routing into the
// existing failure surface — it stamps the same terminal contract
// builds.markFailed writes (and toBuildSummary reads): the build-phase /
// build-message / build-completed-at annotations, spec.done=true, AND
// the build-state=done label. The label is load-bearing, not cosmetic:
// admission's active-build check treats any CR without it as in-flight
// (queueing every later build for the service forever), the poller's
// stuck-build healer skips phase=failed, retention sweeps only select
// build-state=done, and Cancel refuses terminal CRs — so a give-up
// without the label permanently wedged the service's build queue. No
// notify card is emitted — the controller has no notifier wired; the
// operator-facing signal is the error log plus the red build row.
func (s *Service) giveUp(ctx context.Context, u *unstructured.Unstructured, attempts int) {
	if s.Logger != nil {
		s.Logger.Error("buildcontroller: giving up after repeated ensureJob failures — build will NOT run",
			"build", u.GetName(), "ns", u.GetNamespace(), "attempts", attempts,
			"hint", "check apiserver health / RBAC / resource quota, then retrigger the build")
	}
	if s.Kube == nil || s.Kube.Dynamic == nil {
		return
	}
	msg := fmt.Sprintf("build controller could not create the build Job after %d attempts — check server logs, then retrigger", attempts+1)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"failed",%q:%q,%q:%q},"labels":{%q:%q}},"spec":{"done":true}}`,
		builds.AnnBuildPhase,
		builds.AnnBuildMessage, msg,
		builds.AnnBuildCompletedAt, time.Now().UTC().Format(time.RFC3339),
		builds.LabelBuildState, builds.BuildStateDone)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(u.GetNamespace()).
		Patch(pctx, u.GetName(), types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		if s.Logger != nil {
			s.Logger.Warn("buildcontroller: stamp give-up failure state", "build", u.GetName(), "err", err)
		}
	}
}

// ensureJob creates the ServiceAccount + Job for one KusoBuild. The
// SA is created first because the Job's serviceAccountName references
// it; AlreadyExists is success on both. Idempotent against re-fires.
func (s *Service) ensureJob(ctx context.Context, u *unstructured.Unstructured, b *kube.KusoBuild) error {
	ns := u.GetNamespace()
	name := u.GetName()
	ownerRef := metav1.OwnerReference{
		APIVersion: u.GetAPIVersion(),
		Kind:       u.GetKind(),
		Name:       name,
		UID:        u.GetUID(),
		Controller: ptrTrue(),
		// BlockOwnerDeletion stops the apiserver from deleting the
		// KusoBuild CR while the Job still exists. We want the CR to
		// outlive the Job so the build history survives — so leave
		// this false.
	}
	sa := renderServiceAccount(name, ns, ownerRef)
	if _, err := s.Kube.Clientset.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create sa: %w", err)
	}
	job := renderJob(name, ns, b, ownerRef)
	if _, err := s.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

// decodeBuild turns an unstructured CR into our typed KusoBuild.
// The runtime DefaultUnstructuredConverter handles the JSON-tagged
// field hop without an intermediate marshal.
func decodeBuild(u *unstructured.Unstructured) (*kube.KusoBuild, error) {
	var b kube.KusoBuild
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func ptrTrue() *bool          { v := true; return &v }
func ptrFalse() *bool         { v := false; return &v }
func ptrInt32(v int32) *int32 { return &v }
func ptrInt64(v int64) *int64 { return &v }

// strategyOf normalises the build strategy. Empty defaults to
// dockerfile, matching the chart's default in values.yaml.
func strategyOf(b *kube.KusoBuild) string {
	if b == nil {
		return "dockerfile"
	}
	switch strings.ToLower(strings.TrimSpace(b.Spec.Strategy)) {
	case "nixpacks":
		return "nixpacks"
	case "buildpacks":
		return "buildpacks"
	case "static":
		return "static"
	default:
		return "dockerfile"
	}
}

// repoPath returns the in-repo subdirectory, defaulting to "." (the
// chart's `default "."` filter).
func repoPath(b *kube.KusoBuild) string {
	if b == nil || b.Spec.Repo == nil || b.Spec.Repo.Path == "" {
		return "."
	}
	return b.Spec.Repo.Path
}

// hasCache reports whether a cache PVC was attached to the build.
func hasCache(b *kube.KusoBuild) bool {
	return b != nil && b.Spec.Cache != nil && b.Spec.Cache.PVCName != ""
}

// resourceRequirements maps the CR's resources block to the kube
// shape, falling back to the chart's old defaults. We resolve quantity
// strings here so a malformed value (which the API admin couldn't
// have set, since the kuso-server boundary validates them) fails
// the Job create with a clear error rather than a chart-render
// failure.
func resourceRequirements(b *kube.KusoBuild) (corev1.ResourceRequirements, error) {
	reqCPU := defaultCPURequest
	reqMem := defaultMemRequest
	limCPU := defaultCPULimit
	limMem := defaultMemLimit
	if b != nil && b.Spec.Resources != nil {
		if r := b.Spec.Resources.Requests; r != nil {
			if r.CPU != "" {
				reqCPU = r.CPU
			}
			if r.Memory != "" {
				reqMem = r.Memory
			}
		}
		if l := b.Spec.Resources.Limits; l != nil {
			if l.CPU != "" {
				limCPU = l.CPU
			}
			if l.Memory != "" {
				limMem = l.Memory
			}
		}
	}
	parse := func(name, v string) (resource.Quantity, error) {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return q, fmt.Errorf("%s=%q: %w", name, v, err)
		}
		return q, nil
	}
	rc, err := parse("requests.cpu", reqCPU)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	rm, err := parse("requests.memory", reqMem)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	lc, err := parse("limits.cpu", limCPU)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	lm, err := parse("limits.memory", limMem)
	if err != nil {
		return corev1.ResourceRequirements{}, err
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    rc,
			corev1.ResourceMemory: rm,
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    lc,
			corev1.ResourceMemory: lm,
		},
	}, nil
}

// kusoBuildLabels mirrors _helpers.tpl's "kusobuild.labels" set.
// Server-go's build poller selects on these (specifically build-state)
// so the labels must round-trip identically.
//
// `app.kubernetes.io/instance` is critical: every log-stream + cancel
// + drift path selects pods on `instance=<build-name>`. The helm
// chart emitted this via the standard "kusobuild.labels" helper
// because Helm sets it automatically from .Release.Name; the v0.10.0
// Go controller has to set it explicitly. Without it `kubectl get
// pods -l app.kubernetes.io/instance=<build>` returns zero rows
// and the Deployments tab shows "build pod hasn't started yet" for
// every build.
func kusoBuildLabels(b *kube.KusoBuild, buildName string) map[string]string {
	out := map[string]string{
		"app.kubernetes.io/name":       "kusobuild",
		"app.kubernetes.io/component":  "kusobuild",
		"app.kubernetes.io/managed-by": "kuso",
		"app.kubernetes.io/instance":   buildName,
		// Build pods MUST reach github.com (and other public git/
		// package registries) for the clone + nixpacks deps phases.
		// The kusoproject default-deny NetworkPolicy that landed in
		// v0.13 only allows public egress for pods carrying this
		// opt-in label. Without it, the build pod's `git clone`
		// hangs against a deny-egress and times out 30s in.
		"kuso.sislelabs.com/network-egress-public": "true",
	}
	if b == nil {
		return out
	}
	if b.Spec.Project != "" {
		out["kuso.sislelabs.com/project"] = b.Spec.Project
	}
	if b.Spec.Service != "" {
		// Same defensive treatment as build-ref below. The service FQN is
		// "<project>-<service>", and validateProjectName allows 40 chars
		// while serviceNameRE allows 32 — so it can reach 73, past kube's
		// 63-byte label ceiling. Stamped raw, the apiserver rejects the
		// whole Job create and no build pod ever starts.
		out["kuso.sislelabs.com/service"] = sanitizeLabelValue(b.Spec.Service)
	}
	if b.Spec.Ref != "" {
		// Defensive: a label VALUE must be alphanumeric plus '-', '_',
		// '.' (≤63 chars, alnum at both ends). Refs are normally already
		// slug-safe (builds.shortRef slugifies synthetic branch refs at
		// creation), but a raw ref carrying a '/' — e.g. a branch like
		// "deploy/kuso" — would make the whole Job create fail k8s
		// validation, so no build pod ever starts. Sanitize here so a
		// bad ref degrades the label rather than bricking the build.
		out["kuso.sislelabs.com/build-ref"] = sanitizeLabelValue(b.Spec.Ref)
	}
	return out
}

// sanitizeLabelValue coerces an arbitrary string into a valid kube
// label value: lowercase alnum plus '-'/'_'/'.', trimmed to 63 chars
// with alphanumeric ends. Illegal runes collapse to '-'.
func sanitizeLabelValue(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		default:
			out = append(out, '-')
		}
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return strings.Trim(string(out), "-_.")
}

// _ keeps intstr alive — referenced via Job spec parallelism shape
// below if we ever switch from completions=1 to completions=N.
var _ = intstr.IntOrString{}
