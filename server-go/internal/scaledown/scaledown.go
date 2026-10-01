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
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

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
	for i := range envs {
		env := &envs[i]
		svc, ok := svcByKey[env.Namespace+"/"+env.Spec.Service]
		if !ok {
			continue // orphan env: no service, no policy
		}
		pol := resolvePolicy(projByName[env.Spec.Project], svc, env)
		w.evaluateEnv(ctx, svc, env, pol)
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

func (w *Watcher) evaluateEnv(ctx context.Context, svc *kube.KusoService, env *kube.KusoEnvironment, pol Policy) {
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
				return
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
		return
	}

	if !pol.Eligible() {
		return
	}
	if r, ok := w.deploymentReplicas(ctx, ns, envName); !ok || r == 0 {
		return // missing, or already asleep / pre-build hold
	}

	idleMin := pol.AfterMinutes
	active, err := w.requestsInWindow(ctx, ns, envName, idleMin)
	if err != nil {
		// Prometheus unreachable / no data — fail safe by NOT scaling
		// down (better a warm pod than a wrongly-slept app).
		w.Logger.Warn("scaledown: prometheus query", "env", envName, "err", err)
		return
	}
	if active > 0 {
		return // had traffic in the window → still in use
	}

	// HIGH-5a: an activator-routed env's traffic flows through
	// kuso-activator, not the app's own traefik service, so the counter
	// above reads 0. The activator stamps a last-activity annotation;
	// honor it so a busy env is not wrongly slept.
	if w.recentlyActive(ctx, ns, envName, idleMin) {
		return
	}

	// Don't sleep a service while one of its crons/runs is mid-flight.
	// Cron- and run-spawned Jobs carry kuso.sislelabs.com/service=<fqn>.
	if w.hasActiveJobs(ctx, ns, svc.Name) {
		return
	}

	// Last gate, checked against the live object rather than the env CR:
	// the Ingress must already route through the activator. Otherwise
	// (operator lag, old chart, CRD pruned autoSleep) the next request
	// hits a 0-endpoint Service and 503s with nothing to wake it.
	if !w.routedViaActivator(ctx, ns, envName) {
		w.Logger.Info("scaledown: idle env not yet routed via kuso-activator; leaving it up", "env", envName)
		return
	}

	if err := w.scaleToZero(ctx, ns, envName); err != nil {
		w.Logger.Warn("scaledown: scale to zero", "env", envName, "err", err)
		return
	}
	w.Logger.Info("scaledown: slept idle env", "env", envName, "production", pol.Production, "idleMinutes", idleMin)
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
			if p.Backend.Service != nil && activatorBackend(p.Backend.Service.Name) {
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
	v := env.Annotations[LastActivityAnnotation]
	if v == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return false
	}
	return w.now().Sub(ts) < time.Duration(idleMin)*time.Minute
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
func (w *Watcher) scaleToZero(ctx context.Context, ns, envName string) error {
	// Capture the pre-sleep replica count from the live Deployment (the
	// source of truth for current capacity) so wake restores it exactly.
	prior := 1
	if dep, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, envName, metav1.GetOptions{}); err == nil {
		if dep.Spec.Replicas != nil && *dep.Spec.Replicas > 0 {
			prior = int(*dep.Spec.Replicas)
		}
	}

	patch := []byte(`{"spec":{"replicas":0}}`)
	if _, err := w.Kube.Clientset.AppsV1().Deployments(ns).Patch(
		ctx, envName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch deployment: %w", err)
	}
	if _, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(e *kube.KusoEnvironment) error {
		// Stash the pre-sleep count on the CR before zeroing, so the
		// activator can restore it on wake (HIGH-5b).
		if e.Annotations == nil {
			e.Annotations = map[string]string{}
		}
		e.Annotations[PreSleepReplicasAnnotation] = strconv.Itoa(prior)
		e.Spec.SetReplicaCount(0)
		return nil
	}); err != nil {
		// Non-fatal: the Deployment is already at 0; this just prevents
		// the operator from reverting on its next reconcile.
		w.Logger.Warn("scaledown: persist replicaCount=0", "env", envName, "err", err)
	}
	return nil
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
