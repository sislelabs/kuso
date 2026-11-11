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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// wakeOpTimeout bounds a single detached doWake (the Deployment scale
// patch + env-CR replicaCount update). It runs off the request context so
// a client giving up can't abort the scale-up, but must not run unbounded:
// a hang would leak the waking[key] entry and wedge every future wake for
// that service. 30s is generous for two API writes against a healthy
// apiserver and short enough that a stuck one clears well within a
// subsequent request's hold budget.
const wakeOpTimeout = 30 * time.Second

// Activator is the HTTP handler that wakes scaled-to-zero services and
// proxies the request once they are Ready.
type Activator struct {
	kc     *kube.Client
	logger *slog.Logger

	// holdTimeout bounds how long a single request waits for the target
	// to become Ready before we give up and serve a "still starting"
	// page. Container cold start (image already on the node) is usually
	// 1-5s. The default is 60s, not 30s: the dominant cause of a dropped
	// request (and therefore a dropped webhook — see docs/EDIT_SAFETY.md
	// sleep caveat) is a cold image pull on a node that doesn't yet have
	// the image, which can exceed 30s. Holding longer converts those into
	// slow-but-delivered requests instead of 503s. Override with
	// KUSO_ACTIVATOR_HOLD_SECONDS for clusters with unusually slow pulls.
	holdTimeout time.Duration
	// pollInterval is how often we re-check the Deployment's readiness
	// while holding a request.
	pollInterval time.Duration

	// wakeMu serializes the "is a wake already in flight for this
	// service" bookkeeping so N simultaneous first-hits trigger exactly
	// one scale-up and then all proxy once Ready.
	wakeMu sync.Mutex
	waking map[string]*wakeState // key: ns/name

	// hosts resolves Host → env from a periodically refreshed env
	// snapshot that keeps serving through apiserver errors (hostindex.go).
	hosts *hostResolver
	// missLog / lookupErrLog rate-limit the per-request WARN lines for
	// unclaimed hosts and failed lookups.
	missLog, lookupErrLog *logLimiter

	// proxyTransport is the cold-start-tolerant transport used for the
	// reverse proxy (short dial timeout + retry on dial races).
	proxyTransport http.RoundTripper

	// activityMu / lastStamp throttle the per-env last-activity annotation
	// write (recordActivity) to at most once per activityStampInterval, so
	// a busy env doesn't generate an API write per request.
	activityMu sync.Mutex
	lastStamp  map[string]time.Time

	// readyMu / readyAt remember when each env (ns/name) was last seen
	// with a ready endpoint, so the per-request fast path doesn't LIST
	// EndpointSlices on every proxied request. A ClusterRole without
	// endpointslices watch rules out an informer; see fastReady.
	readyMu sync.Mutex
	readyAt map[string]time.Time

	// inflightMu / inflight count proxied requests per env (ns/name) that
	// are still open. A WebSocket or SSE stream stamps activity once at
	// its start; KeepActive re-stamps every env with an open stream so
	// scaledown doesn't sleep it mid-connection. Drain reads the total.
	inflightMu sync.Mutex
	inflight   map[string]int

	// held counts requests waiting on a wake; maxHeld caps it.
	held    atomic.Int64
	maxHeld int64
	// heldByEnv caps held requests per env (maxHeldPerEnv) so one broken
	// host that never becomes ready can't take the whole held budget from
	// every other sleeping app.
	heldMu        sync.Mutex
	heldByEnv     map[string]int64
	maxHeldPerEnv int64

	// podFail caches the newest pod's terminal start failure per env
	// (podfail.go). podCheckOff latches when the activator's RBAC lacks
	// pods list, so an older ClusterRole just loses the fail-fast.
	podFailMu   sync.Mutex
	podFail     map[string]podFailEntry
	podCheckOff atomic.Bool

	// nowFn is time.Now, overridable in tests. Read via now().
	nowFn func() time.Time
}

// now returns the activator's clock, defaulting to time.Now when unset.
func (a *Activator) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
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
	hold := resolveHoldTimeout()
	a := &Activator{
		kc:             kc,
		logger:         logger,
		holdTimeout:    hold,
		pollInterval:   250 * time.Millisecond,
		waking:         map[string]*wakeState{},
		missLog:        &logLimiter{every: 10 * time.Second},
		lookupErrLog:   &logLimiter{every: 10 * time.Second},
		proxyTransport: newProxyTransport(hold),
		lastStamp:      map[string]time.Time{},
		readyAt:        map[string]time.Time{},
		inflight:       map[string]int{},
		maxHeld:        resolveMaxHeld(),
		heldByEnv:      map[string]int64{},
		maxHeldPerEnv:  resolveMaxHeldPerEnv(),
		podFail:        map[string]podFailEntry{},
		nowFn:          time.Now,
	}
	a.hosts = &hostResolver{
		// Every env kind: non-production envs (named, env-group clones,
		// previews) sleep by default, and a stopped env of any kind routes
		// here for the branded stopped page.
		list: func(ctx context.Context) ([]kube.KusoEnvironment, error) { return kc.ListKusoEnvironments(ctx, "") },
		now:  a.now,
	}
	return a
}

// resolveMaxHeldPerEnv returns KUSO_ACTIVATOR_MAX_HELD_PER_ENV (min 1),
// default 100: plenty for a burst of first hits on one app, a tenth of
// the process-wide budget.
func resolveMaxHeldPerEnv() int64 {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KUSO_ACTIVATOR_MAX_HELD_PER_ENV"))); err == nil && v >= 1 {
		return int64(v)
	}
	return 100
}

// resolveMaxHeld returns KUSO_ACTIVATOR_MAX_HELD (min 1), default 1000.
// A held request costs a goroutine plus its connection buffers, so 1000
// stays well inside the activator's memory limit.
func resolveMaxHeld() int64 {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KUSO_ACTIVATOR_MAX_HELD"))); err == nil && v >= 1 {
		return int64(v)
	}
	return 1000
}

// resolveHoldTimeout returns the per-request wake hold budget:
// KUSO_ACTIVATOR_HOLD_SECONDS (clamped to 5..300) if set and parseable,
// else 60s. Kept generous so a cold image pull becomes a slow request
// rather than a 503 that a webhook sender may never retry.
func resolveHoldTimeout() time.Duration {
	const def = 60 * time.Second
	v := strings.TrimSpace(os.Getenv("KUSO_ACTIVATOR_HOLD_SECONDS"))
	if v == "" {
		return def
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 5 {
		return def
	}
	if secs > 300 {
		secs = 300
	}
	return time.Duration(secs) * time.Second
}

// Handler returns the http.Handler that fronts scaled-to-zero traffic.
func (a *Activator) Handler() http.Handler {
	mux := http.NewServeMux()
	// Activator's own health endpoint so kube can probe it without
	// triggering a wake — but only for probe hosts. A user's host is the
	// app's /healthz: answering it here told uptime monitors a sleeping
	// app was healthy, and never woke it.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !isActivatorProbeHost(r.Host) {
			a.serve(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", a.serve)
	return mux
}

// isActivatorProbeHost reports whether a request is addressed to the
// activator itself (kubelet probes by pod IP, in-cluster callers by
// service name) rather than to a user's app host routed here by traefik.
func isActivatorProbeHost(hostport string) bool {
	host := hostOnly(hostport)
	if host == "" || host == "localhost" || net.ParseIP(host) != nil {
		return true
	}
	return !strings.Contains(host, ".") || strings.HasSuffix(host, ".svc") || strings.Contains(host, ".svc.")
}

func (a *Activator) serve(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if host == "" {
		http.Error(w, "activator: missing host", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	env, ns, stopped, err := a.resolveByHost(ctx, host)
	if errors.Is(err, errHostNotFound) {
		if ok, muted := a.missLog.allow(a.now()); ok {
			a.logger.Warn("activator: no env claims host", "host", host, "suppressed", muted)
		}
		// Unknown host — no kuso service claims it. This is what an
		// unmatched domain lands on when traefik's catch-all routes it
		// here (or a stray request to the activator IP). Serve a branded
		// 404 for browsers (content-negotiated) instead of a bare
		// "404 page not found" — nothing we can wake.
		writeActivatorPage(w, r, http.StatusNotFound, notFoundPageHTML, "No kuso service is configured for this domain.\n")
		return
	}
	if err != nil {
		if ok, muted := a.lookupErrLog.allow(a.now()); ok {
			a.logger.Warn("activator: resolve host failed", "host", host, "err", err, "suppressed", muted)
		}
		// The lookup failed (apiserver down, no snapshot yet): we don't
		// know the host is unclaimed, so a cacheable "no service here" 404
		// would be a lie that crawlers and uptime checks act on.
		w.Header().Set("Retry-After", "5")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "service temporarily unavailable, please retry", http.StatusServiceUnavailable)
		return
	}

	// Hard stop: the env is deliberately down and must NOT be woken by
	// traffic (unlike sleep). Serve a clear, branded 503 instead of
	// scaling it up. No Retry-After — the caller shouldn't poll a stopped
	// service into waking; it stays down until an operator starts it.
	// Content-negotiate: browsers get the styled page, everything else
	// (curl, health checks, APIs) gets terse text.
	if stopped {
		writeActivatorPage(w, r, http.StatusServiceUnavailable, stoppedPageHTML, "This service is stopped.\n")
		return
	}

	// Wake (coalesced) and wait until Ready, bounded by holdTimeout.
	wctx, cancel := context.WithTimeout(ctx, a.holdTimeout)
	defer cancel()
	if err := a.wakeAndWait(wctx, ns, env); err != nil {
		a.logger.Warn("activator: wake failed", "ns", ns, "env", env, "err", err)
		var sf *startFailure
		if errors.As(err, &sf) {
			// The new pod is in a state it won't leave on its own (bad
			// image, crash loop, missing secret). Holding the full budget
			// only to 503 hid the real problem; name it instead. The reason
			// is a kube waiting-reason constant, never the image name.
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "service failed to start ("+sf.reason+"); its latest deploy needs attention", http.StatusBadGateway)
			return
		}
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
		a.forgetReady(ns, env)
		rw.Header().Set("Retry-After", "2")
		http.Error(rw, "service is starting, please retry", http.StatusServiceUnavailable)
	}
	// Record activity so scaledown can tell this env is NOT idle. The
	// app's own traefik counter reads zero for sleep-routed envs (all
	// their traffic flows through kuso-activator, not the app's Service),
	// so scaledown can't measure idleness from Prometheus (HIGH-5a). The
	// activator IS in-path for 100% of this traffic, so it's the correct
	// place to stamp a last-activity marker. Throttled to at most once per
	// activityStampInterval per env to avoid an API write per request.
	a.recordActivity(ctx, ns, env)
	done := a.beginProxy(ns, env)
	defer done()
	// The ready cache can say "up" while scaledown is zeroing the env.
	// The first refused dial re-triggers the wake instead of retrying
	// against a 0-endpoint Service for the whole budget with nothing
	// scaling it back up.
	r = r.WithContext(withDialRetryHook(ctx, func() {
		a.forgetReady(ns, env)
		a.kickWake(ns, env)
	}))
	proxy.ServeHTTP(w, r)
}

// beginProxy registers an open proxied request for ns/env and returns
// the func that unregisters it.
func (a *Activator) beginProxy(ns, env string) func() {
	key := ns + "/" + env
	a.inflightMu.Lock()
	a.inflight[key]++
	a.inflightMu.Unlock()
	return func() {
		a.inflightMu.Lock()
		if a.inflight[key] <= 1 {
			delete(a.inflight, key)
		} else {
			a.inflight[key]--
		}
		a.inflightMu.Unlock()
	}
}

// InFlight returns the number of proxied requests still open, including
// upgraded (WebSocket) connections, which http.Server.Shutdown does not
// track.
func (a *Activator) InFlight() int {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	n := 0
	for _, c := range a.inflight {
		n += c
	}
	return n
}

// openEnvs returns the ns/name keys with at least one open proxied request.
func (a *Activator) openEnvs() []string {
	a.inflightMu.Lock()
	defer a.inflightMu.Unlock()
	out := make([]string, 0, len(a.inflight))
	for k := range a.inflight {
		out = append(out, k)
	}
	return out
}

// KeepActive re-stamps last-activity for every env with an open proxied
// connection until ctx ends. Without it a WebSocket/SSE stream counts as
// one request at its start and the env is slept under it once the idle
// window passes. The tick is half the stamp interval so recordActivity's
// throttle, not tick alignment, sets the write rate.
func (a *Activator) KeepActive(ctx context.Context) {
	t := time.NewTicker(activityStampInterval / 2)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.stampOpen(ctx)
		}
	}
}

func (a *Activator) stampOpen(ctx context.Context) {
	for _, key := range a.openEnvs() {
		if ns, env, ok := strings.Cut(key, "/"); ok {
			a.recordActivity(ctx, ns, env)
		}
	}
}

// kickWake starts a coalesced wake for ns/name without waiting on it.
func (a *Activator) kickWake(ns, name string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.holdTimeout)
		defer cancel()
		if err := a.wakeAndWait(ctx, ns, name); err != nil {
			a.logger.Warn("activator: re-wake after refused dial", "ns", ns, "env", name, "err", err)
		}
	}()
}

type dialRetryHookKey struct{}

func withDialRetryHook(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, dialRetryHookKey{}, fn)
}

func dialRetryHook(ctx context.Context) func() {
	fn, _ := ctx.Value(dialRetryHookKey{}).(func())
	return fn
}

// activityStampInterval throttles how often the activator writes the
// last-activity annotation for a given env.
const activityStampInterval = 30 * time.Second

// activityWriteTimeout bounds one detached last-activity write. It is
// shorter than activityStampInterval so at most one write per env is in
// flight at a time.
const activityWriteTimeout = 10 * time.Second

// recordActivity stamps LastActivityAnnotation on the env CR with the
// current time, throttled per-env. The write runs off the request path
// (detached from the request ctx, bounded by activityWriteTimeout).
// Best-effort: a missed stamp just means scaledown falls back to its
// Prometheus check.
func (a *Activator) recordActivity(ctx context.Context, ns, env string) {
	key := ns + "/" + env
	now := a.now()
	a.activityMu.Lock()
	last, ok := a.lastStamp[key]
	if ok && now.Sub(last) < activityStampInterval {
		a.activityMu.Unlock()
		return
	}
	a.lastStamp[key] = now
	a.activityMu.Unlock()

	if a.kc == nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), activityWriteTimeout)
	go func() {
		defer cancel()
		if _, err := a.kc.UpdateKusoEnvironmentWithRetry(wctx, ns, env, func(e *kube.KusoEnvironment) error {
			if e.Annotations == nil {
				e.Annotations = map[string]string{}
			}
			e.Annotations[scaledown.LastActivityAnnotation] = now.UTC().Format(time.RFC3339)
			return nil
		}); err != nil {
			a.logger.Warn("activator: record activity", "ns", ns, "env", env, "err", err)
		}
	}()
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
// both error classes for up to retryBudget (the hold budget) or until the
// client goes away.
func newProxyTransport(retryBudget time.Duration) http.RoundTripper {
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
	return &retryTransport{base: base, wait: 250 * time.Millisecond, budget: retryBudget}
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
	// budget caps how long one RoundTrip keeps retrying dial failures
	// (0 = until the request context ends). The proxied request runs on
	// the client's context, which can live indefinitely, so without a cap
	// an app that refuses forever pins a goroutine per held connection.
	budget time.Duration
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrippers must not mutate the caller's request, so work on a copy.
	req = req.Clone(req.Context())
	var body *untouchedBody
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		body = &untouchedBody{rc: req.Body}
		req.Body = body
	}
	hook := dialRetryHook(req.Context())
	start := time.Now()
	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 && req.GetBody != nil {
			b, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r = req.Clone(req.Context())
			r.Body = b
		}
		resp, err := t.base.RoundTrip(r)
		if err == nil {
			return resp, nil
		}
		if !retriableDialErr(err) || (body != nil && body.touched()) {
			return nil, err
		}
		if attempt == 0 && hook != nil {
			hook()
		}
		if t.budget > 0 && time.Since(start) >= t.budget {
			return nil, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(t.wait):
		}
	}
}

// untouchedBody streams the client's request body to the app instead of
// buffering it in memory. A failed dial never reads the body, so while
// nothing has been read the same body can go out on the next attempt.
// Close is a no-op so the base transport's close-on-error doesn't spend
// it; the http.Server closes the real body when the handler returns.
type untouchedBody struct {
	rc   io.ReadCloser
	read atomic.Bool
}

func (b *untouchedBody) Read(p []byte) (int, error) {
	b.read.Store(true)
	return b.rc.Read(p)
}

func (b *untouchedBody) Close() error { return nil }

func (b *untouchedBody) touched() bool { return b.read.Load() }

// retriableDialErr reports whether err is a cold-start CONNECTION-
// ESTABLISHMENT failure we should retry (connection refused, a dial
// timeout, or no route while kube-proxy catches up). All three mean no
// bytes reached the app, so retrying is safe even for non-idempotent
// methods.
//
// We deliberately do NOT retry "connection reset by peer": a reset can
// arrive AFTER the app has received and processed the request, so
// retrying would re-execute a non-idempotent request (a double POST).
// The cold-start races we care about are all pre-request dial failures,
// which the conditions below already cover.
func retriableDialErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no route to host")
}

// wakeAndWait scales the target Deployment to at least 1 (coalescing
// concurrent callers) and blocks until it reports a Ready replica or
// ctx expires.
func (a *Activator) wakeAndWait(ctx context.Context, ns, name string) error {
	start := a.now()

	// Fast path: already serving? (e.g. another request already woke it,
	// or the route reached us a beat late.) Endpoint-ready, not just
	// deployment-ready, so we never hand back to the proxy before the
	// Service can route.
	if a.fastReady(ctx, ns, name) {
		return nil
	}

	st, inFlight := a.startWake(ns, name)

	// Each held request pins a goroutine and a client connection for up
	// to the hold budget. Past the cap, the wake still runs but the
	// request gets the 503 + Retry-After instead of being held.
	if !a.tryHold(ns + "/" + name) {
		return errHoldCapacity
	}
	defer a.release(ns + "/" + name)

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
	err := a.waitReady(ctx, ns, name)
	if !inFlight && err == nil {
		// One line per real cold start (the owning waiter only), so wake
		// latency is measurable from logs instead of pod timestamps.
		a.logger.Info("activator: woke", "namespace", ns, "service", name, "readyAfter", a.now().Sub(start).Round(time.Millisecond).String())
	}
	return err
}

// errHoldCapacity: too many requests are already held waiting for wakes.
var errHoldCapacity = errors.New("activator: held-request capacity reached")

// tryHold takes one held slot for env key, checking the per-env cap
// before the process-wide one.
func (a *Activator) tryHold(key string) bool {
	a.heldMu.Lock()
	if a.heldByEnv[key] >= a.maxHeldPerEnv {
		a.heldMu.Unlock()
		return false
	}
	a.heldByEnv[key]++
	a.heldMu.Unlock()
	if a.held.Add(1) > a.maxHeld {
		a.held.Add(-1)
		a.releaseEnv(key)
		return false
	}
	return true
}

func (a *Activator) release(key string) {
	a.held.Add(-1)
	a.releaseEnv(key)
}

func (a *Activator) releaseEnv(key string) {
	a.heldMu.Lock()
	if a.heldByEnv[key] <= 1 {
		delete(a.heldByEnv, key)
	} else {
		a.heldByEnv[key]--
	}
	a.heldMu.Unlock()
}

// startWake starts the scale-up for ns/name unless one is already in
// flight, and returns the wake to wait on. inFlight reports whether the
// wake was already running (the caller doesn't own it).
func (a *Activator) startWake(ns, name string) (st *wakeState, inFlight bool) {
	key := ns + "/" + name
	// Coalesce: if a wake is already in flight for this service, wait on
	// its result instead of issuing a second scale-up.
	a.wakeMu.Lock()
	st, inFlight = a.waking[key]
	if !inFlight {
		st = &wakeState{done: make(chan struct{})}
		a.waking[key] = st
		a.wakeMu.Unlock()
		// We own the wake. Run it; broadcast the result; clean up.
		//
		// Detached from the request ctx on purpose — a client that gives up
		// must not abort the scale-up other waiters are blocked on — but
		// BOUNDED by an independent timeout. Without a deadline, a doWake
		// that hangs on a half-dead apiserver connection would never close
		// st.done nor delete waking[key], so every future request for this
		// host would wait on the stale wake until its hold budget expired
		// and the service could never wake until the activator restarted.
		go func() {
			wakeCtx, cancel := context.WithTimeout(context.Background(), wakeOpTimeout)
			defer cancel()
			st.err = a.doWake(wakeCtx, ns, name)
			close(st.done)
			a.wakeMu.Lock()
			delete(a.waking, key)
			a.wakeMu.Unlock()
		}()
	} else {
		a.wakeMu.Unlock()
	}
	return st, inFlight
}

// doWake restores the env to its pre-sleep replica count (idempotent /
// level-triggered). See scaledown.Wake for the ordering contract.
//
// Slept envs this one calls in-cluster are woken alongside it, off the
// request path, so its first server-side call doesn't hit a 0-endpoint
// Service (see scaledown/dependencies.go).
func (a *Activator) doWake(ctx context.Context, ns, name string) error {
	if err := scaledown.Wake(ctx, a.kc, a.logger, ns, name, a.now()); err != nil {
		return err
	}
	go func() {
		dctx, cancel := context.WithTimeout(context.Background(), wakeOpTimeout)
		defer cancel()
		scaledown.WakeDependencies(dctx, a.kc, a.logger, ns, name, a.now())
	}()
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
			a.markReady(ns, name)
			return nil
		}
		if sf := a.startFailed(ctx, ns, name); sf != nil {
			return sf
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

const (
	// readyTTL is how long a ready observation answers the fast path
	// without a LIST. Short: sleep only ever follows a long idle period,
	// and a proxy dial failure drops the entry (forgetReady).
	readyTTL = 2 * time.Second
	// readyStaleOnError is how long a ready observation still counts when
	// the LIST itself fails. An apiserver outage must not read as "asleep"
	// for an env that was serving: proxy optimistically, and if it really
	// is down the refused-dial path re-wakes it. Long, because nothing can
	// sleep an env while the apiserver is unreachable either.
	readyStaleOnError = 15 * time.Minute
)

// fastReady is the per-request readiness check. It serves recent ready
// observations from memory and only LISTs past readyTTL. A LIST error is
// "unknown", not "asleep": a recently-ready env is still proxied.
func (a *Activator) fastReady(ctx context.Context, ns, name string) bool {
	key := ns + "/" + name
	now := a.now()
	a.readyMu.Lock()
	last, seen := a.readyAt[key]
	a.readyMu.Unlock()
	if seen && now.Sub(last) < readyTTL {
		return true
	}
	ready, err := a.endpointReady(ctx, ns, name)
	if err != nil {
		a.logger.Warn("activator: endpoint readiness unknown", "ns", ns, "env", name, "err", err)
		return seen && now.Sub(last) < readyStaleOnError
	}
	if ready {
		a.markReady(ns, name)
	} else {
		a.forgetReady(ns, name)
	}
	return ready
}

func (a *Activator) markReady(ns, name string) {
	a.readyMu.Lock()
	a.readyAt[ns+"/"+name] = a.now()
	a.readyMu.Unlock()
}

func (a *Activator) forgetReady(ns, name string) {
	a.readyMu.Lock()
	delete(a.readyAt, ns+"/"+name)
	a.readyMu.Unlock()
}

// endpointReady reports whether the env's Service (same name as the env)
// has ≥1 ready endpoint address — i.e. kube-proxy will route to a live
// pod. This is the gate the activator waits on before proxying.
func (a *Activator) endpointReady(ctx context.Context, ns, name string) (bool, error) {
	// EndpointSlices, not the deprecated v1 Endpoints API: this runs every
	// 250ms while a request is held, and each Endpoints read logged a
	// deprecation warning.
	slices, err := a.kc.Clientset.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{
		LabelSelector: discoveryv1.LabelServiceName + "=" + name,
	})
	if apierrors.IsForbidden(err) {
		// The updater rolls images but never applies RBAC, so an install
		// upgraded in place may lack the endpointslices rule. Without this
		// fallback every wake timed out into a 503.
		return a.endpointReadyLegacy(ctx, ns, name)
	}
	if err != nil {
		return false, err
	}
	for _, sl := range slices.Items {
		for _, ep := range sl.Endpoints {
			// A nil Ready condition means ready (EndpointSlice API contract).
			if len(ep.Addresses) > 0 && (ep.Conditions.Ready == nil || *ep.Conditions.Ready) {
				return true, nil
			}
		}
	}
	return false, nil
}

// endpointReadyLegacy is endpointReady over the deprecated v1 Endpoints
// API, for installs whose RBAC predates the endpointslices rule.
func (a *Activator) endpointReadyLegacy(ctx context.Context, ns, name string) (bool, error) {
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

// resolveByHost finds the env CR (and its namespace) whose host,
// additional hosts or wildcard covers the request host. errHostNotFound
// means no env claims it; any other error means the lookup failed.
func (a *Activator) resolveByHost(ctx context.Context, host string) (env, ns string, stopped bool, err error) {
	t, stale, err := a.hosts.resolve(ctx, host)
	if err != nil {
		return "", "", false, err
	}
	if stale {
		if ok, muted := a.lookupErrLog.allow(a.now()); ok {
			a.logger.Warn("activator: env list failing, serving last good host map", "suppressed", muted)
		}
	}
	return t.env, t.ns, t.stopped, nil
}

// hostOnly strips any :port suffix from an HTTP Host header.
func hostOnly(h string) string {
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

// writeActivatorPage serves a branded status page for browsers and terse
// text for everything else (curl, health checks, CDNs, APIs). Content-
// negotiated on Accept; missing/odd Accept → text. Cache-Control no-store
// so a start/route change isn't served from a cached error page.
func writeActivatorPage(w http.ResponseWriter, r *http.Request, status int, html []byte, plain string) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(html)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(plain))
	}
}

// activatorPage renders one of the branded pages. Self-contained (inline
// CSS, no external assets) so it renders even though the app itself is
// down: a dark kuso-styled dotted-grid backdrop + a glass card. `badge`,
// `badgeGlyph`, `title`, and `body` are the only per-page differences.
// Built at init (package-level vars below) so we don't rebuild per hit.
func activatorPage(pageTitle, badge, badgeGlyph, heading, body string) []byte {
	return []byte(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>` + pageTitle + `</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  html,body { height:100%; margin:0; }
  body {
    display:flex; align-items:center; justify-content:center;
    min-height:100%;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
    color:#e4e4e7;
    background-color:#0a0a0f;
    background-image: radial-gradient(rgba(255,255,255,0.06) 1px, transparent 1px);
    background-size: 22px 22px;
  }
  .card {
    width: min(92vw, 460px);
    padding: 34px 32px;
    border: 1px solid rgba(255,255,255,0.08);
    border-radius: 14px;
    background: rgba(20,20,28,0.72);
    box-shadow: 0 24px 60px -20px rgba(0,0,0,0.7);
    backdrop-filter: blur(6px);
  }
  .badge {
    display:inline-flex; align-items:center; gap:8px;
    padding:5px 11px; border-radius:8px;
    border:1px solid rgba(148,163,184,0.35);
    background: rgba(100,116,139,0.14);
    color:#cbd5e1; font-size:12px; letter-spacing:0.14em; text-transform:uppercase;
  }
  .badge .glyph { font-size:13px; line-height:1; opacity:0.9; }
  h1 { font-size:19px; font-weight:600; margin:20px 0 8px; letter-spacing:-0.01em; color:#f4f4f5; }
  p  { font-size:13px; line-height:1.6; color:#a1a1aa; margin:0; }
  .foot { margin-top:22px; padding-top:16px; border-top:1px solid rgba(255,255,255,0.06);
          font-size:11px; color:#52525b; letter-spacing:0.04em; }
</style></head>
<body>
  <div class="card">
    <span class="badge"><span class="glyph">` + badgeGlyph + `</span> ` + badge + `</span>
    <h1>` + heading + `</h1>
    <p>` + body + `</p>
    <div class="foot">powered by kuso</div>
  </div>
</body></html>
`)
}

// stoppedPageHTML: a hard-stopped service (deliberately offline, not woken
// by traffic).
var stoppedPageHTML = activatorPage(
	"Service stopped",
	"stopped", "‖",
	"This service is stopped",
	"It has been intentionally taken offline and won't start on its own. Traffic isn't waking it — it stays down until an operator starts it again.",
)

// notFoundPageHTML: an unmatched domain — no kuso service claims this host
// (the catch-all for a domain pointed at kuso with no service, or a base
// domain when only www.<domain> is configured). Restrained, editorial
// design: a near-black page, a large low-contrast "404", a thin rule, and
// a short factual status block. No effects. Self-contained (inline CSS,
// no assets, no JS) so it renders even when the app + DNS are shaky, and
// content-negotiated so curl/health-checks get terse text.
var notFoundPageHTML = []byte(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>404 — no service</title>
<style>
  :root{
    color-scheme: dark;
    --bg:#0b0c0e; --line:#22242a;
    --ink:#e8e9ec; --sub:#9b9ea6; --mute:#616570;
    --sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
  }
  *{box-sizing:border-box;}
  html,body{height:100%;margin:0;}
  body{
    font-family:var(--sans); color:var(--ink); background:var(--bg);
    -webkit-font-smoothing:antialiased; text-rendering:optimizeLegibility;
    display:grid; place-items:center; min-height:100%; padding:32px;
  }
  .wrap{width:100%; max-width:400px;}
  /* oversized, quiet numeral — a background note, not a spectacle */
  .code{
    font-family:var(--mono); font-weight:500;
    font-size:clamp(76px,17vw,132px); line-height:.92; letter-spacing:-.04em;
    color:#2c2f36; margin:0 0 26px; user-select:none;
  }
  h1{font-size:17px; font-weight:600; letter-spacing:-.01em; margin:0 0 8px; color:var(--ink);}
  p{font-size:14px; line-height:1.6; color:var(--sub); margin:0;}
  .rule{height:1px; background:var(--line); margin:26px 0 18px; border:0;}
  /* factual status list — label / value pairs, no chrome */
  dl{margin:0; display:grid; grid-template-columns:auto 1fr; gap:9px 20px;
     font-family:var(--mono); font-size:12.5px;}
  dt{color:var(--mute); white-space:nowrap;}
  dd{margin:0; color:var(--sub);}
  .next{margin-top:22px; font-size:13px; line-height:1.6; color:var(--sub);}
  .next a{color:var(--ink); text-decoration:none; border-bottom:1px solid var(--line);}
  .next a:hover{border-color:var(--sub);}
  .foot{margin-top:34px; font-family:var(--mono); font-size:11px; letter-spacing:.04em; color:#4a4e57;}
  .foot a{color:#6b6f79; text-decoration:none;}
  .foot a:hover{color:var(--sub);}
</style></head>
<body>
  <main class="wrap">
    <div class="code">404</div>
    <h1>No service here.</h1>
    <p>This domain reaches kuso, but nothing is configured to answer it.</p>

    <hr class="rule">
    <dl>
      <dt>status</dt><dd>no route matched</dd>
      <dt>served by</dt><dd>kuso</dd>
    </dl>

    <p class="next">If this domain is yours, <a href="https://github.com/sislelabs/kuso" rel="noreferrer">add a service in kuso</a> and point the hostname at it — otherwise, check the address for a typo.</p>

    <div class="foot"><a href="https://github.com/sislelabs/kuso" rel="noreferrer">kuso</a></div>
  </main>
</body></html>
`)
