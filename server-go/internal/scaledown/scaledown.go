// Package scaledown implements the "scale-down" half of kuso's
// scale-to-zero (the activator in internal/activator is the "scale-up"
// half; see docs/design/SCALE_TO_ZERO.md).
//
// The Watcher is a leader-elected loop: every tick it walks EVERY env of
// every service, resolves the sleep policy (policy.go), and scales an
// env's Deployment to 0 once it has been idle longer than the window.
// Production sleeps only when the service opted in (sleep.enabled);
// every non-production env (named envs, env-group clones, PR previews)
// sleeps by default unless the service set sleep.nonProduction=off. The
// activator wakes a slept env on its next request.
//
// HPA-managed envs sleep too: scaling the target Deployment to 0 puts
// the HPA into Kubernetes' implicit maintenance mode (ScalingActive=
// False, "scaling is disabled since the replica count of the target is
// zero") and it stays paused until the activator raises replicas again.
// The chart omits spec.replicas whenever the HPA is on, so the operator
// never re-stamps it either.
package scaledown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
	"kuso/server/internal/serverstate"
)

const defaultPromURL = "http://kuso-prometheus.kuso.svc.cluster.local:9090"

// DefaultTickInterval is the watcher's tick cadence when Watcher.Tick is
// unset. Exported so main.go can register scaledown in the serverstate
// liveness registry at the cadence it beats. main.go leaves Tick unset,
// so this is the effective interval.
const DefaultTickInterval = time.Minute

// Watcher scales idle sleep-enabled services to zero.
type Watcher struct {
	Kube      *kube.Client
	Namespace string // default kuso namespace (used to resolve per-project ns)
	Logger    *slog.Logger

	// Tick is the evaluation cadence. Defaults to 1 minute.
	Tick time.Duration
	// PromURL overrides the prometheus base URL (KUSO_PROMETHEUS_URL).
	PromURL string

	// Now is time.Now, overridable in tests. Use w.now() to read it.
	Now func() time.Time

	httpc *http.Client

	// secretVals caches the values of the managed Secrets scanned for
	// in-cluster references, keyed ns/name and revalidated by
	// resourceVersion so a tick doesn't GET every Secret again.
	secretVals map[string]cachedSecretVals
}

// now returns the Watcher's clock, defaulting to time.Now when unset so
// callers that build a Watcher as a bare struct literal still work.
func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Run loops until ctx is cancelled. Intended to run under the
// cluster-singleton leader gate so only one replica scales things down.
func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	tick := w.Tick
	if tick <= 0 {
		tick = DefaultTickInterval
	}
	if w.PromURL == "" {
		w.PromURL = defaultPromURL
		if v := os.Getenv("KUSO_PROMETHEUS_URL"); v != "" {
			w.PromURL = v
		}
	}
	if w.httpc == nil {
		w.httpc = &http.Client{Timeout: 5 * time.Second}
	}
	// Disable switch so an operator can turn scale-to-zero enforcement
	// off cluster-wide without un-setting every service's sleep flag.
	if os.Getenv("KUSO_SCALEDOWN_DISABLED") == "true" {
		w.Logger.Info("scaledown disabled via KUSO_SCALEDOWN_DISABLED")
		return
	}

	t := time.NewTicker(tick)
	defer t.Stop()
	w.Logger.Info("scaledown watcher started", "tick", tick.String())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.evaluate(ctx)
			serverstate.LoopHeartbeat(serverstate.LoopScaledown)
		}
	}
}

// evaluate runs one pass over every env.
func (w *Watcher) evaluate(ctx context.Context) {
	// Never scale anything to zero, or route anything new through the
	// activator, unless the thing that wakes it back up is actually
	// alive. kuso-activator is a SEPARATE Deployment
	// (deploy/kuso-activator.yaml) that has historically been down
	// without anyone noticing. Fail safe: skip the whole tick and leave
	// services warm.
	if !w.activatorReady(ctx) {
		w.Logger.Warn("scaledown: kuso-activator has no ready replicas — skipping scale-to-zero this tick (services stay warm; nothing could wake them)")
		return
	}
	// Empty ns → cluster-wide; each object carries its own namespace.
	svcs, err := w.Kube.ListKusoServices(ctx, "")
	if err != nil {
		w.Logger.Warn("scaledown: list services", "err", err)
		return
	}
	envs, err := w.Kube.ListKusoEnvironments(ctx, "")
	if err != nil {
		w.Logger.Warn("scaledown: list environments", "err", err)
		return
	}
	// Projects carry alwaysOn. A failed list must not read as "not
	// alwaysOn" (that would sleep an opted-out project), so skip the tick.
	projs, err := w.Kube.ListKusoProjects(ctx, "")
	if err != nil {
		w.Logger.Warn("scaledown: list projects", "err", err)
		return
	}
	projByName := make(map[string]*kube.KusoProject, len(projs))
	for i := range projs {
		projByName[projs[i].Name] = &projs[i]
	}
	svcByKey := make(map[string]*kube.KusoService, len(svcs))
	for i := range svcs {
		svcByKey[svcs[i].Namespace+"/"+svcs[i].Name] = &svcs[i]
	}
	// Pass 1: per-env decision. Candidates are idle envs that passed every
	// gate; nothing is slept until the dependency pass below.
	// Bounded concurrency and a per-pass deadline: one Prometheus query
	// per env run serially let a slow Prometheus stretch a tick to
	// envs × 5s. An env whose check runs out of time is not a candidate,
	// so the failure mode is "stays awake".
	candidates := map[string]*kube.KusoEnvironment{}
	policies := make(map[string]Policy, len(envs))
	for i := range envs {
		env := &envs[i]
		if svc, ok := svcByKey[env.Namespace+"/"+env.Spec.Service]; ok {
			policies[envKey(env)] = resolvePolicy(projByName[env.Spec.Project], svc, env)
		}
	}
	passCtx, cancel := context.WithTimeout(ctx, evaluatePassBudget)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, evaluateConcurrency)
	)
	for i := range envs {
		env := &envs[i]
		pol, ok := policies[envKey(env)]
		if !ok {
			continue // orphan env: no service, no policy
		}
		svc := svcByKey[env.Namespace+"/"+env.Spec.Service]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if w.evaluateEnv(passCtx, svc, env, pol) {
				mu.Lock()
				candidates[envKey(env)] = env
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Pass 2: in-cluster references (see dependencies.go). Drop every
	// candidate that an awake, non-sleeping env calls, to a fixed point
	// so a chain web → api → auth stays up as a unit. Envs that call each
	// other and are all idle remain candidates and sleep together.
	refs := referrersByTarget(envs, w.secretRefValues(ctx, envs))
	for changed := true; changed; {
		changed = false
		for key := range candidates {
			if by := w.awakeReferrer(ctx, refs[key], candidates); by != "" {
				w.Logger.Debug("scaledown: idle env kept up; an awake env calls it in-cluster", "env", key, "referrer", by)
				delete(candidates, key)
				changed = true
			}
		}
	}
	for _, env := range candidates {
		ns := env.Namespace
		if ns == "" {
			ns = w.Namespace
		}
		if err := w.scaleToZero(ctx, ns, env.Name, policies[envKey(env)].AfterMinutes); err != nil {
			if errors.Is(err, errRecentlyActive) {
				continue
			}
			w.Logger.Warn("scaledown: scale to zero", "env", env.Name, "err", err)
			continue
		}
		pol := policies[envKey(env)]
		w.Logger.Info("scaledown: slept idle env", "env", env.Name, "production", pol.Production, "idleMinutes", pol.AfterMinutes)
	}

	w.wakeReferencedSleepers(ctx, envs, refs, candidates, policies)
}

func envKey(e *kube.KusoEnvironment) string { return e.Namespace + "/" + e.Name }

const (
	evaluateConcurrency = 8
	evaluatePassBudget  = 45 * time.Second
)

// maxDependencyWakesPerTick caps how many slept envs one tick wakes
// because an awake env calls them. Envs slept under an older kuso that
// ignored in-cluster calls are healed gradually rather than all at once.
const maxDependencyWakesPerTick = 3

// awakeReferrer returns the first referrer that is awake (replicas > 0)
// and not itself about to sleep, or "".
func (w *Watcher) awakeReferrer(ctx context.Context, referrers []string, candidates map[string]*kube.KusoEnvironment) string {
	for _, r := range referrers {
		if _, sleeping := candidates[r]; sleeping {
			continue
		}
		ns, name := splitKey(r)
		if n, ok := w.deploymentReplicas(ctx, ns, name); ok && n > 0 {
			return r
		}
	}
	return ""
}

// wakeReferencedSleepers wakes envs scaledown slept that an awake env
// calls in-cluster: those calls are being refused right now and nothing
// else wakes the target. Under the pass-2 rule such an env would not
// have been slept, so the wake doesn't flap.
func (w *Watcher) wakeReferencedSleepers(ctx context.Context, envs []kube.KusoEnvironment, refs map[string][]string, candidates map[string]*kube.KusoEnvironment, policies map[string]Policy) {
	woken := 0
	for i := range envs {
		if woken >= maxDependencyWakesPerTick {
			return
		}
		env := &envs[i]
		key := envKey(env)
		pol, ok := policies[key]
		if !ok || pol.Stopped || env.Spec.Stopped || env.Annotations[PreSleepReplicasAnnotation] == "" || len(refs[key]) == 0 {
			continue
		}
		if r, ok := w.deploymentReplicas(ctx, env.Namespace, env.Name); !ok || r != 0 {
			continue
		}
		by := w.awakeReferrer(ctx, refs[key], candidates)
		if by == "" {
			continue
		}
		if err := Wake(ctx, w.Kube, w.Logger, env.Namespace, env.Name, w.now()); err != nil {
			w.Logger.Warn("scaledown: wake env called by an awake env", "env", key, "err", err)
			continue
		}
		woken++
		w.Logger.Info("scaledown: woke slept env an awake env calls in-cluster", "env", key, "referrer", by)
	}
}

// activatorDeployment is the scale-to-zero wake proxy's Deployment name
// (deploy/kuso-activator.yaml). It lives in the kuso control-plane
// namespace regardless of per-project namespaces.
const activatorDeployment = "kuso-activator"

// activatorReady reports whether the kuso-activator Deployment has at
// least one ready replica. Reads the informer cache when available
// (this runs every tick) and falls back to a live Get. Any failure —
// Deployment missing, apiserver error — reads as NOT ready: without a
// wake path, scaling to zero is guaranteed downtime, while skipping a
// tick just leaves an idle pod warm for another minute.
func (w *Watcher) activatorReady(ctx context.Context) bool {
	ns := w.Namespace
	if ns == "" {
		ns = "kuso"
	}
	if w.Kube.Cache != nil {
		if dep, ok := w.Kube.Cache.GetDeployment(ns, activatorDeployment); ok {
			return dep.Status.ReadyReplicas >= 1
		}
	}
	dep, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, activatorDeployment, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			w.Logger.Warn("scaledown: get activator deployment", "err", err)
		}
		return false
	}
	return dep.Status.ReadyReplicas >= 1
}

// deploymentReplicas returns the env Deployment's spec.replicas, served
// from the informer cache (this runs per env every minute) with a live
// Get fallback. ok=false when the Deployment doesn't exist or can't be read.
func (w *Watcher) deploymentReplicas(ctx context.Context, ns, name string) (int32, bool) {
	dep, ok := w.Kube.Cache.GetDeployment(ns, name)
	if !ok {
		d, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				w.Logger.Warn("scaledown: get deployment", "env", name, "err", err)
			}
			return 0, false
		}
		dep = d
	}
	if dep.Spec.Replicas == nil {
		return 1, true
	}
	return *dep.Spec.Replicas, true
}

// evaluateEnv reconciles one env's routing / wake state and reports
// whether it is idle and may be slept (evaluate makes the final call).
func (w *Watcher) evaluateEnv(ctx context.Context, svc *kube.KusoService, env *kube.KusoEnvironment, pol Policy) bool {
	ns := env.Namespace
	if ns == "" {
		ns = w.Namespace
	}
	envName := env.Name

	// An env asleep under a policy that no longer allows sleep (service
	// opted out, sleep disabled, project alwaysOn) would otherwise stay
	// at 0 behind an Ingress that is about to point back at its own
	// 0-endpoint Service. Wake it BEFORE the routing flips away from the
	// activator. A stopped env stays down: the operator owns that pin.
	if !pol.Allowed && !pol.Stopped && env.Annotations[PreSleepReplicasAnnotation] != "" {
		if r, ok := w.deploymentReplicas(ctx, ns, envName); ok && r == 0 {
			if err := Wake(ctx, w.Kube, w.Logger, ns, envName, w.now()); err != nil {
				w.Logger.Warn("scaledown: wake env no longer allowed to sleep", "env", envName, "err", err)
				return false
			}
			w.Logger.Info("scaledown: woke env no longer allowed to sleep", "env", envName)
		}
	}

	// Reconcile activator routing for the non-production default. On a
	// flip, stop here: the operator needs to re-render the Ingress before
	// this env can safely sleep, and turning routing ON also restarts the
	// idle clock so the env gets a full window behind the activator.
	if env.Spec.AutoSleep != pol.AutoRoute {
		if err := w.setAutoSleep(ctx, ns, envName, pol.AutoRoute); err != nil {
			w.Logger.Warn("scaledown: set autoSleep", "env", envName, "err", err)
		}
		return false
	}

	if !pol.Eligible() {
		return false
	}
	if r, ok := w.deploymentReplicas(ctx, ns, envName); !ok || r == 0 {
		return false // missing, or already asleep / pre-build hold
	}
	// Mid-rollout the old ReplicaSet is the one serving. Scaling to 0
	// empties both and a wake only brings back the newest, so a wedged
	// deploy that was harmless becomes an outage on the next request.
	if w.rolloutInFlight(ctx, ns, envName) {
		w.Logger.Debug("scaledown: idle env has a rollout in flight; leaving it up", "env", envName)
		return false
	}

	idleMin := pol.AfterMinutes
	active, err := w.requestsInWindow(ctx, ns, envName, idleMin)
	if err != nil {
		// Prometheus unreachable / no data — fail safe by NOT scaling
		// down (better a warm pod than a wrongly-slept app).
		w.Logger.Warn("scaledown: prometheus query", "env", envName, "err", err)
		return false
	}
	if active > 0 {
		return false // had traffic in the window → still in use
	}

	// HIGH-5a: an activator-routed env's traffic flows through
	// kuso-activator, not the app's own traefik service, so the counter
	// above reads 0. The activator stamps a last-activity annotation;
	// honor it so a busy env is not wrongly slept.
	if w.recentlyActive(ctx, ns, envName, idleMin) {
		return false
	}

	// Don't sleep a service while one of its crons/runs is mid-flight.
	// Cron- and run-spawned Jobs carry kuso.sislelabs.com/service=<fqn>.
	if w.hasActiveJobs(ctx, ns, svc.Name) {
		return false
	}

	// Last gate, checked against the live object rather than the env CR:
	// the Ingress must already route through the activator. Otherwise
	// (operator lag, old chart, CRD pruned autoSleep) the next request
	// hits a 0-endpoint Service and 503s with nothing to wake it.
	if !w.routedViaActivator(ctx, ns, envName) {
		w.Logger.Info("scaledown: idle env not yet routed via kuso-activator; leaving it up", "env", envName)
		return false
	}
	return true
}

// rolloutInFlight reports whether the env's Deployment is mid-rollout:
// a spec change not yet observed, fewer updated replicas than desired, or
// pods of an older ReplicaSet still running. A read failure counts as in
// flight (stay up).
func (w *Watcher) rolloutInFlight(ctx context.Context, ns, name string) bool {
	dep, ok := w.Kube.Cache.GetDeployment(ns, name)
	if !ok {
		d, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return true
		}
		dep = d
	}
	return deploymentRollingOut(dep)
}

func deploymentRollingOut(dep *appsv1.Deployment) bool {
	want := int32(1)
	if dep.Spec.Replicas != nil {
		want = *dep.Spec.Replicas
	}
	st := dep.Status
	return dep.Generation > st.ObservedGeneration ||
		st.UpdatedReplicas < want ||
		st.Replicas > st.UpdatedReplicas
}

// setAutoSleep writes env.spec.autoSleep. Turning it on also stamps
// last-activity=now so the env gets a full idle window after the Ingress
// moves onto the activator.
func (w *Watcher) setAutoSleep(ctx context.Context, ns, envName string, on bool) error {
	now := w.now()
	_, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(e *kube.KusoEnvironment) error {
		e.Spec.AutoSleep = on
		if on {
			if e.Annotations == nil {
				e.Annotations = map[string]string{}
			}
			e.Annotations[LastActivityAnnotation] = now.UTC().Format(time.RFC3339)
		}
		return nil
	})
	return err
}

// routedViaActivator reports whether the env's main Ingress (named after
// the env) sends its traffic to kuso-activator. Any read failure → false.
func (w *Watcher) routedViaActivator(ctx context.Context, ns, envName string) bool {
	ing, err := w.Kube.Clientset.NetworkingV1().Ingresses(ns).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if p.Backend.Service != nil && activatorBackend(p.Backend.Service.Name, envName) {
				return true
			}
		}
	}
	return false
}

// requestsInWindow returns the number of requests the env's traefik
// service handled over the last idleMin minutes. 0 means idle.
func (w *Watcher) requestsInWindow(ctx context.Context, ns, envName string, idleMin int) (float64, error) {
	q := fmt.Sprintf(
		`sum(increase(traefik_service_requests_total{service=~"%s"}[%dm])) or vector(0)`,
		traefikServiceMatcher(ns, envName), idleMin)
	return w.promInstant(ctx, q)
}

// traefikServiceMatcher matches an env's traefik service label,
// "<namespace>-<envname>-<port>@kubernetes". The "-" after the env name
// keeps preview pr-1 from also counting pr-10..pr-19's traffic (and so
// never going idle).
func traefikServiceMatcher(ns, envName string) string {
	return escapePromLabel(ns+"-"+envName+"-") + ".*@kubernetes"
}

// PreSleepReplicasAnnotation records the replica count an env had just
// before scaledown put it to sleep, so the activator can restore the
// SAME capacity on wake instead of hardcoding 1. Without this, a service
// running at replicaCount=3 came back at 1 after its first idle window
// and stayed there — a silent 3x capacity loss (HIGH-5b).
const PreSleepReplicasAnnotation = "kuso.sislelabs.com/pre-sleep-replicas"

// LastActivityAnnotation records (RFC3339) the last time the activator
// proxied a request for this env. Because a sleep-routed env's traffic
// flows through kuso-activator (not the app's own traefik service), the
// Prometheus request counter reads zero — so this annotation is the
// authoritative "was there recent traffic" signal for the idle check
// (HIGH-5a). Written by the activator's recordActivity, read by
// recentlyActive below.
const LastActivityAnnotation = "kuso.sislelabs.com/last-activity"

// ActivityStampInterval is how often the activator refreshes
// LastActivityAnnotation for an env with ongoing traffic. Every write
// triggers a helm-operator reconcile of the env, so it is minutes apart;
// stampWindow widens short idle windows so a busy env's stamp is never
// older than the window it is checked against.
const ActivityStampInterval = 4 * time.Minute

// stampWindow is the idle window used against LastActivityAnnotation: the
// policy's window, but never shorter than one stamp interval plus a tick
// of margin.
func stampWindow(idleMin int) int {
	floor := int((ActivityStampInterval+time.Minute)/time.Minute) + 1
	if idleMin < floor {
		return floor
	}
	return idleMin
}

// recentlyActive reports whether the activator stamped activity for this
// env within the idle window. A parse failure / missing annotation → not
// recently active (fall through to sleeping), which is safe: the env only
// reaches this check after the Prometheus query already returned 0, and a
// genuinely-busy env gets its annotation refreshed every 30s.
func (w *Watcher) recentlyActive(ctx context.Context, ns, envName string, idleMin int) bool {
	env, err := w.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil || env == nil {
		return false
	}
	return activeSince(env.Annotations[LastActivityAnnotation], w.now(), stampWindow(idleMin))
}

// hasActiveJobs reports whether any cron/run Job for this service is
// currently running (Status.Active > 0). Both the kusocron and kusorun
// charts stamp kuso.sislelabs.com/service=<fqn> on the Job; serviceFQN is
// svc.Name (the fq <project>-<service>). A LIST error fails safe by
// reporting "active" so a transient apiserver hiccup never causes a
// mid-run sleep.
func (w *Watcher) hasActiveJobs(ctx context.Context, ns, serviceFQN string) bool {
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	jobs, err := w.Kube.Clientset.BatchV1().Jobs(ns).List(listCtx, metav1.ListOptions{
		LabelSelector: "kuso.sislelabs.com/service=" + serviceFQN,
	})
	if err != nil {
		w.Logger.Warn("scaledown: list jobs for in-flight guard", "service", serviceFQN, "err", err)
		return true // fail safe: don't sleep on a broken LIST
	}
	for i := range jobs.Items {
		if jobs.Items[i].Status.Active > 0 {
			return true
		}
	}
	return false
}

// scaleToZero patches the Deployment to 0 replicas and persists
// replicaCount=0 on the env CR (so the helm-operator reconcile doesn't
// scale it back up). Before zeroing, it stashes the current replica count
// in an annotation so wake can restore it.
//
// The env CR is written first, as a compare-and-swap on the last-activity
// stamp: the update re-reads the CR and aborts with errRecentlyActive if
// the activator stamped it since the idle check, and a stamp landing
// mid-update makes the write conflict and re-run that check. A request
// that still slips in after the CR write hits a refused dial, which makes
// the activator wake the env again.
func (w *Watcher) scaleToZero(ctx context.Context, ns, envName string, idleMin int) error {
	// Capture the pre-sleep replica count from the live Deployment (the
	// source of truth for current capacity) so wake restores it exactly.
	prior := 1
	if dep, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, envName, metav1.GetOptions{}); err == nil {
		if dep.Spec.Replicas != nil && *dep.Spec.Replicas > 0 {
			prior = int(*dep.Spec.Replicas)
		}
	}

	now := w.now()
	if _, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(e *kube.KusoEnvironment) error {
		if activeSince(e.Annotations[LastActivityAnnotation], now, stampWindow(idleMin)) {
			return errRecentlyActive
		}
		// Stash the pre-sleep count on the CR before zeroing, so the
		// activator can restore it on wake (HIGH-5b).
		if e.Annotations == nil {
			e.Annotations = map[string]string{}
		}
		e.Annotations[PreSleepReplicasAnnotation] = strconv.Itoa(prior)
		e.Spec.SetReplicaCount(0)
		return nil
	}); err != nil {
		return fmt.Errorf("persist replicaCount=0: %w", err)
	}

	patch := []byte(`{"spec":{"replicas":0}}`)
	if _, err := w.Kube.Clientset.AppsV1().Deployments(ns).Patch(
		ctx, envName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}
	return nil
}

// errRecentlyActive aborts scaleToZero when the env saw traffic after the
// idle check.
var errRecentlyActive = errors.New("scaledown: env became active")

// activeSince reports whether an RFC3339 last-activity stamp falls within
// the idle window ending at now. Missing or unparseable → false.
func activeSince(stamp string, now time.Time, idleMin int) bool {
	if stamp == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false
	}
	return now.Sub(ts) < time.Duration(idleMin)*time.Minute
}

// promInstant runs a PromQL instant query and returns the first scalar
// result (0 if no series).
func (w *Watcher) promInstant(ctx context.Context, query string) (float64, error) {
	q := url.Values{}
	q.Set("query", query)
	u := w.PromURL + "/api/v1/query?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := w.httpc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("prometheus: status %d", resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Status != "success" || len(body.Data.Result) == 0 {
		return 0, nil
	}
	// value is [unixTime, "<float-as-string>"]
	s, ok := body.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, nil
	}
	return f, nil
}

// escapePromLabel escapes regex metacharacters in a label value used
// inside a =~ matcher.
func escapePromLabel(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '.', '+', '*', '?', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			out = append(out, '\\', c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}
