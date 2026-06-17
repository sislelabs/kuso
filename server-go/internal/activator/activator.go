// Package activator implements kuso's scale-to-zero request-holding
// proxy. It is the "scale-up" half of scale-to-zero (the idle detector
// in internal/scaledown is the "scale-down" half).
//
// Flow (see docs/design/SCALE_TO_ZERO.md):
//
//	1. A sleep-enabled service idles → its Deployment is scaled to 0.
//	2. traefik routes requests for a 0-replica service to this activator
//	   (via an errors-middleware fallback or an operator route flip).
//	3. The activator resolves the target env from the Host header,
//	   scales its Deployment to 1 (coalescing concurrent first-hits into
//	   one scale-up), waits — bounded — until the Deployment reports a
//	   Ready replica, then reverse-proxies the held request to the app's
//	   in-cluster Service.
//
// The activator is stateless and horizontally scalable; all shared
// state (desired replicas) lives on the Deployment object, so two
// activator replicas racing to wake the same service converge.
package activator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Activator is the HTTP handler that wakes scaled-to-zero services and
// proxies the request once they are Ready.
type Activator struct {
	kc     *kube.Client
	logger *slog.Logger

	// holdTimeout bounds how long a single request waits for the target
	// to become Ready before we give up and serve a "still starting"
	// page. Container cold start (image already on the node) is usually
	// 1-5s; 30s covers a slow start without wedging the client forever.
	holdTimeout time.Duration
	// pollInterval is how often we re-check the Deployment's readiness
	// while holding a request.
	pollInterval time.Duration

	// wakeMu serializes the "is a wake already in flight for this
	// service" bookkeeping so N simultaneous first-hits trigger exactly
	// one scale-up and then all proxy once Ready.
	wakeMu sync.Mutex
	waking map[string]*wakeState // key: ns/name

	// hostCache memoizes host → (env, ns) so we don't filter the full
	// env list on every request. Short TTL so a host move/rename heals
	// quickly; the activator is off the hot path once an app is awake,
	// so staleness only ever affects the first-hit-after-idle.
	hostMu    sync.RWMutex
	hostCache map[string]hostEntry
	hostTTL   time.Duration
}

type hostEntry struct {
	env, ns string
	at      time.Time
}

type wakeState struct {
	done chan struct{} // closed when the target is Ready (or wake failed)
	err  error
}

// New constructs an Activator over the given kube client.
func New(kc *kube.Client, logger *slog.Logger) *Activator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Activator{
		kc:           kc,
		logger:       logger,
		holdTimeout:  30 * time.Second,
		pollInterval: 250 * time.Millisecond,
		waking:       map[string]*wakeState{},
		hostCache:    map[string]hostEntry{},
		hostTTL:      30 * time.Second,
	}
}

// Handler returns the http.Handler that fronts scaled-to-zero traffic.
func (a *Activator) Handler() http.Handler {
	mux := http.NewServeMux()
	// Activator's own health endpoint so traefik/kube can probe it
	// without triggering a wake.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", a.serve)
	return mux
}

func (a *Activator) serve(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if host == "" {
		http.Error(w, "activator: missing host", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	env, ns, err := a.resolveByHost(ctx, host)
	if err != nil {
		a.logger.Warn("activator: resolve host", "host", host, "err", err)
		// Unknown host — nothing we can wake. 404 rather than a confusing
		// 502, so a stray request to the activator's IP doesn't look like
		// an app error.
		http.Error(w, "activator: no service for host", http.StatusNotFound)
		return
	}

	// Wake (coalesced) and wait until Ready, bounded by holdTimeout.
	wctx, cancel := context.WithTimeout(ctx, a.holdTimeout)
	defer cancel()
	if err := a.wakeAndWait(wctx, ns, env); err != nil {
		a.logger.Warn("activator: wake failed", "ns", ns, "env", env, "err", err)
		// Cold start exceeded the budget (slow image pull, crash loop).
		// 503 + Retry-After so browsers/CDNs back off and retry rather
		// than hammering a starting pod.
		w.Header().Set("Retry-After", "5")
		http.Error(w, "service is starting, please retry in a moment", http.StatusServiceUnavailable)
		return
	}

	// Ready — reverse-proxy to the app's in-cluster Service. The Service
	// name equals the env CR name; cluster DNS resolves it.
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s.%s.svc.cluster.local:80", env, ns),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, perr error) {
		a.logger.Warn("activator: proxy error", "ns", ns, "env", env, "err", perr)
		rw.Header().Set("Retry-After", "2")
		http.Error(rw, "service is starting, please retry", http.StatusServiceUnavailable)
	}
	proxy.ServeHTTP(w, r)
}

// wakeAndWait scales the target Deployment to at least 1 (coalescing
// concurrent callers) and blocks until it reports a Ready replica or
// ctx expires.
func (a *Activator) wakeAndWait(ctx context.Context, ns, name string) error {
	key := ns + "/" + name

	// Fast path: already Ready? (e.g. the route flipped a beat late, or
	// another request already woke it.)
	if ready, _ := a.deploymentReady(ctx, ns, name); ready {
		return nil
	}

	// Coalesce: if a wake is already in flight for this service, wait on
	// its result instead of issuing a second scale-up.
	a.wakeMu.Lock()
	st, inFlight := a.waking[key]
	if !inFlight {
		st = &wakeState{done: make(chan struct{})}
		a.waking[key] = st
		a.wakeMu.Unlock()
		// We own the wake. Run it; broadcast the result; clean up.
		go func() {
			st.err = a.doWake(context.Background(), ns, name)
			close(st.done)
			a.wakeMu.Lock()
			delete(a.waking, key)
			a.wakeMu.Unlock()
		}()
	} else {
		a.wakeMu.Unlock()
	}

	// Whether we own the wake or not, wait for readiness (the owner's
	// doWake only triggers the scale; readiness is observed here so
	// every waiter gets unblocked the moment a pod is Ready).
	select {
	case <-st.done:
		if st.err != nil {
			return st.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return a.waitReady(ctx, ns, name)
}

// doWake bumps the Deployment to 1 replica (idempotent / level-triggered).
// It patches the Deployment directly for an immediate effect, then best-
// effort bumps the env CR's spec.replicaCount so the helm-operator's next
// reconcile doesn't revert it back to 0.
func (a *Activator) doWake(ctx context.Context, ns, name string) error {
	if a.kc == nil || a.kc.Clientset == nil {
		return fmt.Errorf("activator: no kube client")
	}
	// Direct Deployment scale → instant. The scale subresource avoids a
	// full update conflict with the operator's reconcile.
	patch := []byte(`{"spec":{"replicas":1}}`)
	_, err := a.kc.Clientset.AppsV1().Deployments(ns).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("scale deployment: %w", err)
	}
	// Persist desired replicas on the env CR so a later operator
	// reconcile (which renders spec.replicas from .Values.replicaCount)
	// doesn't clobber us back to 0. Best-effort: the Deployment patch is
	// the authoritative wake; this just stops the revert.
	if _, uerr := a.kc.UpdateKusoEnvironmentWithRetry(ctx, ns, name, func(e *kube.KusoEnvironment) error {
		if e.Spec.ReplicaCountValue() < 1 {
			e.Spec.SetReplicaCount(1)
		}
		return nil
	}); uerr != nil {
		a.logger.Warn("activator: persist replicaCount", "ns", ns, "env", name, "err", uerr)
	}
	return nil
}

// waitReady polls until the Deployment reports ≥1 Ready replica.
func (a *Activator) waitReady(ctx context.Context, ns, name string) error {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		if ready, _ := a.deploymentReady(ctx, ns, name); ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *Activator) deploymentReady(ctx context.Context, ns, name string) (bool, error) {
	dep, err := a.kc.Clientset.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	return readyReplicas(dep) >= 1, nil
}

func readyReplicas(dep *appsv1.Deployment) int32 {
	if dep == nil {
		return 0
	}
	return dep.Status.ReadyReplicas
}

// resolveByHost finds the env CR (and its namespace) whose Host or
// AdditionalHosts matches the request host. Returns (envName, namespace).
// Memoized with a short TTL — resolution only runs on the first hit
// after idle, but a hot CDN can send a burst, so we avoid re-filtering
// the full env list per request.
func (a *Activator) resolveByHost(ctx context.Context, host string) (string, string, error) {
	a.hostMu.RLock()
	if e, ok := a.hostCache[host]; ok && time.Since(e.at) < a.hostTTL {
		a.hostMu.RUnlock()
		return e.env, e.ns, nil
	}
	a.hostMu.RUnlock()

	// Production envs only — preview envs are short-lived and shouldn't
	// be scaled to zero (they get GC'd on TTL instead). Empty namespace
	// lists cluster-wide; the env CR carries its own namespace. Routes
	// through the cached typed-list (informer-backed when warm → a slice
	// filter, not a network round-trip).
	envs, err := a.kc.ListKusoEnvironments(ctx, "")
	if err != nil {
		return "", "", err
	}
	for i := range envs {
		e := &envs[i]
		if e.Spec.Kind != "" && e.Spec.Kind != "production" {
			continue
		}
		if hostMatches(host, e) {
			a.hostMu.Lock()
			a.hostCache[host] = hostEntry{env: e.Name, ns: e.Namespace, at: time.Now()}
			a.hostMu.Unlock()
			return e.Name, e.Namespace, nil
		}
	}
	return "", "", fmt.Errorf("no env matches host %q", host)
}

func hostMatches(host string, e *kube.KusoEnvironment) bool {
	if strings.EqualFold(e.Spec.Host, host) {
		return true
	}
	for _, h := range e.Spec.AdditionalHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// hostOnly strips any :port suffix from an HTTP Host header.
func hostOnly(h string) string {
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}
