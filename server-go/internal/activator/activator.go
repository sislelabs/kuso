// Package activator implements kuso's scale-to-zero request-holding
// proxy. It is the "scale-up" half of scale-to-zero (the idle detector
// in internal/scaledown is the "scale-down" half).
//
// Flow (see docs/design/SCALE_TO_ZERO.md):
//
//  1. A sleep-enabled service idles → its Deployment is scaled to 0.
//  2. traefik routes requests for a 0-replica service to this activator
//     (via an errors-middleware fallback or an operator route flip).
//  3. The activator resolves the target env from the Host header,
//     scales its Deployment to 1 (coalescing concurrent first-hits into
//     one scale-up), waits — bounded — until the Deployment reports a
//     Ready replica, then reverse-proxies the held request to the app's
//     in-cluster Service.
//
// The activator is stateless and horizontally scalable; all shared
// state (desired replicas) lives on the Deployment object, so two
// activator replicas racing to wake the same service converge.
package activator

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

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

	// proxyTransport is the cold-start-tolerant transport used for the
	// reverse proxy (short dial timeout + retry on dial races).
	proxyTransport http.RoundTripper
}

type hostEntry struct {
	env, ns string
	// stopped mirrors the env's spec.stopped. A stopped env must NOT be
	// woken by traffic (that's the whole point of a hard stop vs sleep),
	// so the handler serves a "service stopped" 503 instead of waking.
	// Cached alongside env/ns; the short hostTTL bounds staleness after
	// a start/stop toggle.
	stopped bool
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
		kc:             kc,
		logger:         logger,
		holdTimeout:    30 * time.Second,
		pollInterval:   250 * time.Millisecond,
		waking:         map[string]*wakeState{},
		hostCache:      map[string]hostEntry{},
		hostTTL:        30 * time.Second,
		proxyTransport: newProxyTransport(),
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

	env, ns, stopped, err := a.resolveByHost(ctx, host)
	if err != nil {
		a.logger.Warn("activator: resolve host", "host", host, "err", err)
		// Unknown host — nothing we can wake. 404 rather than a confusing
		// 502, so a stray request to the activator's IP doesn't look like
		// an app error.
		http.Error(w, "activator: no service for host", http.StatusNotFound)
		return
	}

	// Hard stop: the env is deliberately down and must NOT be woken by
	// traffic (unlike sleep). Serve a clear 503 instead of scaling it up.
	// No Retry-After — the caller shouldn't poll a stopped service into
	// waking; it stays down until an operator starts it.
	if stopped {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("This service is stopped.\n"))
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
	proxy.Transport = a.proxyTransport
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, perr error) {
		a.logger.Warn("activator: proxy error", "ns", ns, "env", env, "err", perr)
		rw.Header().Set("Retry-After", "2")
		http.Error(rw, "service is starting, please retry", http.StatusServiceUnavailable)
	}
	proxy.ServeHTTP(w, r)
}

// newProxyTransport builds the transport the activator proxies through.
// Two cold-start hazards motivate the design:
//   - "connection refused": endpoint ready but the pod's listener hasn't
//     called accept() yet.
//   - "i/o timeout": the Endpoints object has an address but kube-proxy
//     hasn't programmed the Service ClusterIP rule yet, so the dial to
//     the ClusterIP black-holes.
//
// Each dial gets a SHORT timeout so a black-holed ClusterIP fails fast
// instead of hanging the whole hold budget, and retryTransport retries
// both error classes until the request context (the hold deadline)
// expires.
func newProxyTransport() http.RoundTripper {
	dialer := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		// Don't reuse a connection for a service that may have just been
		// scaled — fresh dial each time during the cold-start window.
		DisableKeepAlives: false,
	}
	return &retryTransport{base: base, wait: 250 * time.Millisecond}
}

// retryTransport retries connection-establishment failures during the
// cold-start window — "connection refused" (listener not up yet) and
// dial "i/o timeout" (Service ClusterIP not yet programmed). Both mean
// no bytes reached the app, so retrying is safe even for non-idempotent
// methods. It retries until the request context (the activator's hold
// deadline) expires.
type retryTransport struct {
	base http.RoundTripper
	wait time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for {
		resp, err := t.base.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		if !retriableDialErr(err) {
			return nil, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(t.wait):
		}
	}
}

// retriableDialErr reports whether err is a cold-start dial failure we
// should retry (connection refused, or a dial timeout / no route while
// kube-proxy catches up).
func retriableDialErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "connection reset")
}

// wakeAndWait scales the target Deployment to at least 1 (coalescing
// concurrent callers) and blocks until it reports a Ready replica or
// ctx expires.
func (a *Activator) wakeAndWait(ctx context.Context, ns, name string) error {
	key := ns + "/" + name

	// Fast path: already serving? (e.g. another request already woke it,
	// or the route reached us a beat late.) Endpoint-ready, not just
	// deployment-ready, so we never hand back to the proxy before the
	// Service can route.
	if ready, _ := a.endpointReady(ctx, ns, name); ready {
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

// waitReady polls until the target Service has at least one READY
// endpoint address. We check Endpoints, NOT Deployment.ReadyReplicas:
// a fresh scale-up can report a ready replica a beat before kube-proxy
// programs the Service endpoint, and proxying in that window yields
// "connection refused". The Service endpoint being ready is the true
// "the proxied request will land on a live pod" signal.
func (a *Activator) waitReady(ctx context.Context, ns, name string) error {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()
	for {
		if ready, _ := a.endpointReady(ctx, ns, name); ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// endpointReady reports whether the env's Service (same name as the env)
// has ≥1 ready endpoint address — i.e. kube-proxy will route to a live
// pod. This is the gate the activator waits on before proxying.
func (a *Activator) endpointReady(ctx context.Context, ns, name string) (bool, error) {
	ep, err := a.kc.Clientset.CoreV1().Endpoints(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	for _, ss := range ep.Subsets {
		if len(ss.Addresses) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// resolveByHost finds the env CR (and its namespace) whose Host or
// AdditionalHosts matches the request host. Returns (envName, namespace).
// Memoized with a short TTL — resolution only runs on the first hit
// after idle, but a hot CDN can send a burst, so we avoid re-filtering
// the full env list per request.
func (a *Activator) resolveByHost(ctx context.Context, host string) (env, ns string, stopped bool, err error) {
	a.hostMu.RLock()
	if e, ok := a.hostCache[host]; ok && time.Since(e.at) < a.hostTTL {
		a.hostMu.RUnlock()
		return e.env, e.ns, e.stopped, nil
	}
	a.hostMu.RUnlock()

	// Production envs only — preview envs are short-lived and shouldn't
	// be scaled to zero (they get GC'd on TTL instead). Empty namespace
	// lists cluster-wide; the env CR carries its own namespace. Routes
	// through the cached typed-list (informer-backed when warm → a slice
	// filter, not a network round-trip).
	envs, lerr := a.kc.ListKusoEnvironments(ctx, "")
	if lerr != nil {
		return "", "", false, lerr
	}
	for i := range envs {
		e := &envs[i]
		if e.Spec.Kind != "" && e.Spec.Kind != "production" {
			continue
		}
		if hostMatches(host, e) {
			a.hostMu.Lock()
			a.hostCache[host] = hostEntry{env: e.Name, ns: e.Namespace, stopped: e.Spec.Stopped, at: time.Now()}
			a.hostMu.Unlock()
			return e.Name, e.Namespace, e.Spec.Stopped, nil
		}
	}
	return "", "", false, fmt.Errorf("no env matches host %q", host)
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
