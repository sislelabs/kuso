// Package scaledown implements the "scale-down" half of kuso's
// scale-to-zero (the activator in internal/activator is the "scale-up"
// half; see docs/design/SCALE_TO_ZERO.md).
//
// The Watcher is a leader-elected loop: every tick it finds
// sleep-enabled services, asks prometheus how many requests their
// production env served over the idle window, and scales the env's
// Deployment to 0 when it has been idle longer than
// sleep.afterMinutes. The activator wakes it back up on the next
// request.
//
// Guard: a service with sleep.wakeOn.excludePaths is never scaled to 0
// (kube can't route per-path inside one Deployment, so "any path must
// stay warm" means the whole service stays warm — matching
// effectiveScaleMin's behaviour on the write path).
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
)

const defaultPromURL = "http://kuso-prometheus.kuso.svc.cluster.local:9090"

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
		tick = time.Minute
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
		}
	}
}

// evaluate runs one pass: scale idle sleep-enabled services to 0.
func (w *Watcher) evaluate(ctx context.Context) {
	// List sleep-enabled services across all kuso namespaces. Empty ns
	// → cluster-wide; each service carries its own namespace via labels.
	svcs, err := w.Kube.ListKusoServices(ctx, "")
	if err != nil {
		w.Logger.Warn("scaledown: list services", "err", err)
		return
	}
	for i := range svcs {
		svc := &svcs[i]
		if !sleepEligible(svc) {
			continue
		}
		w.evaluateService(ctx, svc)
	}
}

// sleepEligible reports whether a service is a candidate for scale-to-
// zero: sleep enabled, and no must-stay-warm paths.
func sleepEligible(svc *kube.KusoService) bool {
	if svc.Spec.Sleep == nil || !svc.Spec.Sleep.Enabled {
		return false
	}
	// A hard-stopped service is already pinned to 0 by the operator and
	// must NOT be woken by traffic; scaledown has nothing to do and
	// touching its Deployment would only race the operator's pin.
	if svc.Spec.Stopped {
		return false
	}
	// wakeOn.excludePaths → keep warm (same guard as effectiveScaleMin).
	if w := svc.Spec.Sleep.WakeOn; w != nil && len(w.ExcludePaths) > 0 {
		return false
	}
	return true
}

// hpaManaged reports whether the service's env Deployment is owned by an
// HPA, in which case scale-to-zero must not touch its replica count.
//
// The signal is the same one the env chart uses to decide whether to
// render an HPA (see projects.autoscalingFromScale): autoscaling is ON
// exactly when the user asked for headroom, i.e. scale.Max is greater
// than scale.Min. We read it off the service spec — the authoritative
// source — rather than probing the live HPA, which keeps this a pure,
// cache-free check on the object we already have in hand.
func hpaManaged(svc *kube.KusoService) bool {
	s := svc.Spec.Scale
	if s == nil {
		return false
	}
	return s.Max > s.MinValue()
}

func (w *Watcher) evaluateService(ctx context.Context, svc *kube.KusoService) {
	ns := svc.Namespace
	if ns == "" {
		ns = w.Namespace
	}
	// Production env name follows the <service>-production convention;
	// the service CR's own name is the fq <project>-<service>.
	envName := svc.Name + "-production"

	// Serve the Deployment from the informer cache — this runs per
	// sleep-enabled service every minute, and the cluster cache already
	// watches Deployments, so a live Get per service is needless apiserver
	// chatter that grows with service count. Fall back to a live Get only
	// when the cache isn't synced.
	dep, ok := w.Kube.Cache.GetDeployment(ns, envName)
	if !ok {
		d, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, envName, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				w.Logger.Warn("scaledown: get deployment", "env", envName, "err", err)
			}
			return
		}
		dep = d
	}
	// Already at 0 (or scaling down) — nothing to do.
	if dep.Spec.Replicas != nil && *dep.Spec.Replicas == 0 {
		return
	}
	// If an HPA owns this Deployment we must not fight it — scale-to-zero
	// requires autoscaling OFF (the chart only stamps spec.replicas when
	// the HPA is absent; with an HPA present, patching replicas=0 just gets
	// reverted on the HPA's next sync and thrashes the Deployment).
	//
	// The old check looked for the "autoscaling.alpha.kubernetes.io/
	// conditions" annotation ON THE DEPLOYMENT — but that annotation only
	// ever lands on the HPA object, never the target Deployment, so it was
	// always absent and hpaManaged was always false. Result: an
	// autoscaling+sleep service got scaled to 0 anyway. Detect ownership
	// from the SERVICE SPEC instead, which is the exact same signal the
	// chart uses to decide whether to render an HPA: autoscalingFromScale
	// returns non-nil (Enabled) precisely when scale.Max > scale.Min. That
	// mirrors projects.autoscalingFromScale.
	if hpaManaged(svc) {
		return
	}

	idleMin := svc.Spec.Sleep.AfterMinutes
	if idleMin <= 0 {
		idleMin = 30
	}

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

	// HIGH-5a: the Prometheus query above reads the app's OWN traefik
	// service, but a sleep-enabled env's ingress backend is kuso-activator
	// — so the app's counter never increments and `active` is always 0.
	// The activator, which IS in-path for every request, stamps a
	// last-activity annotation; honor it so a busy env is not wrongly
	// slept. If the annotation is within the idle window, the env is live.
	if w.recentlyActive(ctx, ns, envName, idleMin) {
		return
	}

	// Don't sleep a service while one of its crons/runs is mid-flight.
	// Cron- and run-spawned Jobs carry kuso.sislelabs.com/service=<fqn>
	// (svc.Name is the fq <project>-<service>). Scaling the app
	// Deployment to 0 doesn't kill the Job pod itself, but a kind=service
	// cron shares the app's conn secrets and may depend on the app being
	// warm; more importantly, sleeping mid-run makes the "is this service
	// in use" signal lie. If any labelled Job is still Active, skip.
	if w.hasActiveJobs(ctx, ns, svc.Name) {
		return
	}

	// Idle. Scale the Deployment to 0 and stamp the env CR so the
	// operator's reconcile keeps it there until the activator wakes it.
	if err := w.scaleToZero(ctx, ns, envName); err != nil {
		w.Logger.Warn("scaledown: scale to zero", "env", envName, "err", err)
		return
	}
	w.Logger.Info("scaledown: slept idle service", "env", envName, "idleMinutes", idleMin)
}

// requestsInWindow returns the number of requests the env's traefik
// service handled over the last idleMin minutes. 0 means idle.
func (w *Watcher) requestsInWindow(ctx context.Context, ns, envName string, idleMin int) (float64, error) {
	// Matches the label format the env-metrics endpoint uses:
	//   service="<namespace>-<envname>-http@kubernetes"
	matcher := escapePromLabel(ns+"-"+envName) + ".*@kubernetes"
	q := fmt.Sprintf(
		`sum(increase(traefik_service_requests_total{service=~"%s"}[%dm])) or vector(0)`,
		matcher, idleMin)
	return w.promInstant(ctx, q)
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
