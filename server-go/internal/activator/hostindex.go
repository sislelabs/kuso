package activator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// errHostNotFound means the env list was read and no env claims the host:
// the one case that may answer 404. Any other resolve error means the
// lookup itself failed and must answer 503, never "no service here".
var errHostNotFound = errors.New("activator: no env matches host")

const (
	// hostIndexTTL bounds how stale a successful env snapshot is served
	// before the next request re-LISTs. It is also how long a just-created
	// env's host (or a start/stop toggle) takes to be seen.
	hostIndexTTL = 5 * time.Second
	// hostIndexRetry spaces LIST attempts while the apiserver is failing, so
	// an outage doesn't turn every request into a doomed LIST.
	hostIndexRetry = 2 * time.Second
	// hostListTimeout bounds one env LIST.
	hostListTimeout = 10 * time.Second
)

type hostTarget struct {
	env, ns string
	// stopped mirrors spec.stopped: a stopped env serves the stopped page
	// and is never woken by traffic.
	stopped bool
}

// hostIndex is one env LIST turned into host lookups. It is rebuilt
// whole, so the request path never LISTs per Host header and a flood of
// random hosts costs a map miss instead of a cluster-wide LIST.
type hostIndex struct {
	exact map[string]hostTarget
	wild  map[string]hostTarget // "*.<suffix>" → target
	// activity is each env's last-activity stamp (ns/name → time) as of
	// this snapshot, so the activator can skip a stamp another replica
	// already wrote.
	activity map[string]time.Time
	at       time.Time
}

func buildHostIndex(envs []kube.KusoEnvironment, at time.Time) *hostIndex {
	idx := &hostIndex{exact: map[string]hostTarget{}, wild: map[string]hostTarget{}, activity: map[string]time.Time{}, at: at}
	add := func(m map[string]hostTarget, h string, t hostTarget) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return
		}
		if _, taken := m[h]; !taken {
			m[h] = t
		}
	}
	for i := range envs {
		e := &envs[i]
		if ts, err := time.Parse(time.RFC3339, e.Annotations[scaledown.LastActivityAnnotation]); err == nil {
			idx.activity[e.Namespace+"/"+e.Name] = ts
		}
		// Internal services and workers have no Ingress, but the catch-all
		// route still lands their host here; proxying would expose them.
		if e.Spec.Internal || e.Spec.Runtime == "worker" {
			continue
		}
		t := hostTarget{env: e.Name, ns: e.Namespace, stopped: e.Spec.Stopped}
		hosts := append([]string{e.Spec.Host}, e.Spec.AdditionalHosts...)
		for _, h := range hosts {
			add(idx.exact, h, t)
			if strings.HasPrefix(h, "*.") {
				add(idx.wild, h, t)
			}
		}
		for _, w := range e.Spec.WildcardDomains {
			add(idx.wild, w.Host, t)
		}
	}
	return idx
}

// lookup resolves host: an exact host wins over any wildcard, and
// wildcards have one-label semantics ("*.example.com" covers
// "a.example.com", not "example.com" or "a.b.example.com").
func (idx *hostIndex) lookup(host string) (hostTarget, bool) {
	host = strings.ToLower(host)
	if t, ok := idx.exact[host]; ok {
		return t, true
	}
	if dot := strings.IndexByte(host, '.'); dot > 0 {
		if t, ok := idx.wild["*"+host[dot:]]; ok {
			return t, true
		}
	}
	return hostTarget{}, false
}

// hostResolver serves host lookups from the last successful env LIST.
// A LIST failure keeps serving the previous snapshot however old it is:
// an apiserver outage must not turn every activator-routed app into a
// 404 while its pods, kube-proxy and traefik are all still working.
type hostResolver struct {
	list func(ctx context.Context) ([]kube.KusoEnvironment, error)
	now  func() time.Time

	mu      sync.Mutex // serializes refreshes; readers use idxMu
	nextTry time.Time
	lastErr error
	idxMu   sync.RWMutex
	idx     *hostIndex
}

func (r *hostResolver) current() *hostIndex {
	r.idxMu.RLock()
	defer r.idxMu.RUnlock()
	return r.idx
}

// snapshot returns a usable index, refreshing it when older than
// hostIndexTTL. stale reports that the index is past its TTL because the
// refresh failed.
func (r *hostResolver) snapshot(ctx context.Context) (idx *hostIndex, stale bool, err error) {
	now := r.now()
	cur := r.current()
	if cur != nil && now.Sub(cur.at) < hostIndexTTL {
		return cur, false, nil
	}
	if cur != nil {
		// Someone else is refreshing: serve the previous snapshot rather
		// than queue every request behind a slow LIST.
		if !r.mu.TryLock() {
			return cur, false, nil
		}
	} else {
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	cur = r.current()
	if cur != nil && r.now().Sub(cur.at) < hostIndexTTL {
		return cur, false, nil // another request refreshed it while we waited
	}
	if now.Before(r.nextTry) {
		if cur != nil {
			return cur, true, nil
		}
		return nil, false, r.lastErr
	}
	// Detached from the request: one client hanging up must not count as
	// a failed refresh for everyone else.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hostListTimeout)
	defer cancel()
	envs, lerr := r.list(lctx)
	if lerr != nil {
		r.lastErr = lerr
		r.nextTry = r.now().Add(hostIndexRetry)
		if cur != nil {
			return cur, true, nil
		}
		return nil, false, lerr
	}
	fresh := buildHostIndex(envs, r.now())
	r.idxMu.Lock()
	r.idx = fresh
	r.idxMu.Unlock()
	r.lastErr = nil
	r.nextTry = time.Time{}
	return fresh, false, nil
}

// lastActivity returns the env's last-activity stamp from the current
// snapshot without refreshing it.
func (r *hostResolver) lastActivity(key string) (time.Time, bool) {
	idx := r.current()
	if idx == nil {
		return time.Time{}, false
	}
	ts, ok := idx.activity[key]
	return ts, ok
}

// resolve returns the target for host, errHostNotFound when a snapshot
// exists and no env claims it, or the LIST error when there has never
// been a successful snapshot.
func (r *hostResolver) resolve(ctx context.Context, host string) (hostTarget, bool, error) {
	idx, stale, err := r.snapshot(ctx)
	if err != nil {
		return hostTarget{}, false, fmt.Errorf("activator: list envs: %w", err)
	}
	t, ok := idx.lookup(host)
	if !ok {
		return hostTarget{}, stale, errHostNotFound
	}
	return t, stale, nil
}

// logLimiter lets one log line through per interval and counts the rest,
// so a flood of junk Host headers can't turn into one WARN line each.
type logLimiter struct {
	every time.Duration
	mu    sync.Mutex
	last  time.Time
	muted int
}

// allow reports whether to log now, and how many calls were suppressed
// since the last allowed one.
func (l *logLimiter) allow(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		l.muted++
		return false, 0
	}
	n := l.muted
	l.last, l.muted = now, 0
	return true, n
}
