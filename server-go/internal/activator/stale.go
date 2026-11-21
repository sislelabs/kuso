package activator

import (
	"bytes"
	"container/list"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Serve-stale-while-waking (spec.sleep.serveStale). While proxying for an
// opted-in env the activator keeps a copy of responses that are safe to
// replay to anyone; when the env is asleep, an anonymous GET/HEAD for a
// URL it has a copy of gets that copy at once while the wake runs.
// Per-replica and in-memory: a miss on the other replica falls back to
// the ordinary hold-and-proxy.

const (
	// staleMaxEntry is the largest body kept. Past it the capture is
	// abandoned mid-stream; the client still gets the whole response.
	staleMaxEntry = 1 << 20
	// staleRecaptureAfter skips re-copying a URL whose entry is this fresh,
	// so a busy page costs one copy a minute rather than one per request.
	staleRecaptureAfter = time.Minute
	// staleHeaderOverhead approximates per-entry bookkeeping in the byte
	// accounting, so many tiny entries can't exceed the cap by much.
	staleHeaderOverhead = 256
)

type staleEntry struct {
	key, env string
	status   int
	header   http.Header
	body     []byte
	stored   time.Time
	size     int64
	all, per *list.Element
}

type staleEnvLRU struct {
	lru   *list.List
	bytes int64
}

// staleCache is a byte-capped LRU shared by every env, with a per-env cap
// so one large site can't evict every other env's pages.
type staleCache struct {
	maxBytes, maxPerEnv int64
	ttl                 time.Duration

	mu      sync.Mutex
	entries map[string]*staleEntry
	lru     *list.List // front = most recent
	envs    map[string]*staleEnvLRU
	bytes   int64

	// inflight bounds bytes held by captures still streaming, so N
	// concurrent large responses can't each pin staleMaxEntry.
	inflight    atomic.Int64
	maxInflight int64
}

func newStaleCache(maxBytes, maxPerEnv, maxInflight int64, ttl time.Duration) *staleCache {
	return &staleCache{
		maxBytes: maxBytes, maxPerEnv: maxPerEnv, maxInflight: maxInflight, ttl: ttl,
		entries: map[string]*staleEntry{}, lru: list.New(), envs: map[string]*staleEnvLRU{},
	}
}

// staleKey keys on the Accept-Encoding bucket as well as the URL, so a
// body stored compressed is only ever replayed to a client that sent the
// same set of accepted encodings.
func staleKey(env string, r *http.Request) string {
	return env + "\x00" + strings.ToLower(hostOnly(r.Host)) + "\x00" + r.URL.RequestURI() + "\x00" + encodingBucket(r.Header.Values("Accept-Encoding"))
}

// encodingBucket is the sorted set of content codings the client accepts
// (q>0), "" for identity only.
func encodingBucket(vals []string) string {
	set := map[string]bool{}
	for _, v := range vals {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" || name == "identity" {
				continue
			}
			if q, ok := strings.CutPrefix(strings.ReplaceAll(strings.ToLower(params), " ", ""), "q="); ok {
				if f, err := strconv.ParseFloat(q, 64); err == nil && f == 0 {
					continue
				}
			}
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// anonymousRequest reports a request with no credentials: only those may
// be stored or answered from the shared copy.
func anonymousRequest(r *http.Request) bool {
	for k := range r.Header {
		switch lk := strings.ToLower(k); {
		case lk == "cookie", lk == "upgrade", strings.Contains(lk, "auth"),
			strings.Contains(lk, "token"), strings.Contains(lk, "api-key"),
			strings.Contains(lk, "apikey"), strings.Contains(lk, "session"):
			return false
		}
	}
	return true
}

// staleCapturable reports whether a proxied request's response may be
// stored. The response itself is checked by staleStorable.
func staleCapturable(r *http.Request) bool {
	return r.Method == http.MethodGet && anonymousRequest(r)
}

// staleServable reports whether a request may be answered from a stored
// copy. kuso's own uptime checks are held as before: a stale 200 would
// report a broken app as up.
func staleServable(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && anonymousRequest(r) && countsAsActivity(r)
}

// staleStorable reports whether a response is safe to replay to any
// anonymous visitor.
func staleStorable(resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK || len(resp.Header.Values("Set-Cookie")) > 0 {
		return false
	}
	if resp.ContentLength > staleMaxEntry {
		return false
	}
	// The app must opt in to shared caching: a response is only stored
	// when it says any cache may keep it (public, or a positive s-maxage /
	// max-age). Silence is not consent: a page personalised by a header we
	// don't know about would otherwise replay to every visitor.
	shared := false
	for _, cc := range resp.Header.Values("Cache-Control") {
		for _, d := range strings.Split(cc, ",") {
			name, val, _ := strings.Cut(strings.TrimSpace(d), "=")
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "private", "no-store", "no-cache":
				return false
			case "public":
				shared = true
			case "s-maxage", "max-age":
				if n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(val), `"`)); err == nil && n > 0 {
					shared = true
				}
			}
		}
	}
	if !shared {
		return false
	}
	// Vary on a header the storing request didn't send describes the
	// variant for requests without it (Next.js varies on RSC/router
	// headers a plain page load never carries); serveStale only replays
	// it to requests that also lack them. Vary: * matches no request.
	for _, name := range varyNames(resp.Header) {
		if name == "*" || resp.Request == nil || resp.Request.Header.Get(name) != "" {
			return false
		}
	}
	return true
}

// varyNames lists the Vary header names other than Accept-Encoding, which
// staleKey already folds into the cache key.
func varyNames(h http.Header) []string {
	var names []string
	for _, v := range h.Values("Vary") {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" && !strings.EqualFold(f, "Accept-Encoding") {
				names = append(names, f)
			}
		}
	}
	return names
}

// varyMatches reports whether r selects the variant stored in e: r must
// lack every header the stored response varied on.
func varyMatches(e *staleEntry, r *http.Request) bool {
	for _, name := range varyNames(e.header) {
		if r.Header.Get(name) != "" {
			return false
		}
	}
	return true
}

// fresh reports whether key has an entry stored within d.
func (c *staleCache) fresh(key string, now time.Time, d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	return ok && now.Sub(e.stored) < d
}

// get returns a live entry and marks it recently used; an expired one is
// dropped.
func (c *staleCache) get(key string, now time.Time) (*staleEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if now.Sub(e.stored) >= c.ttl {
		c.removeLocked(e)
		return nil, false
	}
	c.lru.MoveToFront(e.all)
	c.envs[e.env].lru.MoveToFront(e.per)
	return e, true
}

func (c *staleCache) put(e *staleEntry) {
	e.size = int64(len(e.body)) + int64(len(e.key)) + staleHeaderOverhead
	for k, vs := range e.header {
		e.size += int64(len(k))
		for _, v := range vs {
			e.size += int64(len(v))
		}
	}
	if e.size > c.maxPerEnv || e.size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[e.key]; ok {
		c.removeLocked(old)
	}
	pe := c.envs[e.env]
	if pe == nil {
		pe = &staleEnvLRU{lru: list.New()}
		c.envs[e.env] = pe
	}
	for pe.bytes+e.size > c.maxPerEnv {
		c.removeLocked(pe.lru.Back().Value.(*staleEntry))
	}
	for c.bytes+e.size > c.maxBytes {
		c.removeLocked(c.lru.Back().Value.(*staleEntry))
	}
	// The per-env loop may have emptied pe and dropped it from the map.
	c.envs[e.env] = pe
	e.all = c.lru.PushFront(e)
	e.per = pe.lru.PushFront(e)
	pe.bytes += e.size
	c.bytes += e.size
	c.entries[e.key] = e
}

func (c *staleCache) removeLocked(e *staleEntry) {
	c.lru.Remove(e.all)
	pe := c.envs[e.env]
	pe.lru.Remove(e.per)
	pe.bytes -= e.size
	if pe.lru.Len() == 0 {
		delete(c.envs, e.env)
	}
	c.bytes -= e.size
	delete(c.entries, e.key)
}

// capture wraps resp.Body so the bytes the proxy streams to the client
// are copied on the side, and stored once the body reaches EOF intact.
func (c *staleCache) capture(resp *http.Response, env, key string, now func() time.Time) {
	header := resp.Header.Clone()
	header.Del("Content-Length")
	resp.Body = &captureBody{ReadCloser: resp.Body, c: c, done: func(body []byte) {
		c.put(&staleEntry{key: key, env: env, status: resp.StatusCode, header: header, body: body, stored: now()})
	}}
}

// captureBody tees reads into buf. Exceeding staleMaxEntry or the
// in-flight budget abandons the copy; the stream itself is untouched.
type captureBody struct {
	io.ReadCloser
	c        *staleCache
	buf      bytes.Buffer
	reserved int64
	dropped  bool
	finished bool
	done     func([]byte)
}

func (b *captureBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && !b.dropped && !b.finished {
		switch {
		case b.buf.Len()+n > staleMaxEntry:
			b.abandon()
		case b.c.inflight.Add(int64(n)) > b.c.maxInflight:
			b.c.inflight.Add(-int64(n))
			b.abandon()
		default:
			b.reserved += int64(n)
			b.buf.Write(p[:n])
		}
	}
	if err == io.EOF && !b.dropped && !b.finished {
		b.finished = true
		body := bytes.Clone(b.buf.Bytes())
		b.release()
		b.done(body)
	}
	return n, err
}

func (b *captureBody) Close() error {
	if !b.finished {
		b.abandon()
	}
	return b.ReadCloser.Close()
}

func (b *captureBody) abandon() {
	b.dropped = true
	b.release()
}

func (b *captureBody) release() {
	b.c.inflight.Add(-b.reserved)
	b.reserved = 0
	b.buf = bytes.Buffer{}
}

// writeStale replays e: Age says how old the copy is, X-Kuso-Stale marks
// it, and no-store keeps browsers and CDNs from pinning it.
func writeStale(w http.ResponseWriter, r *http.Request, e *staleEntry, now time.Time) {
	h := w.Header()
	for k, vs := range e.header {
		h[k] = append([]string(nil), vs...)
	}
	age := int64(now.Sub(e.stored) / time.Second)
	if age < 0 {
		age = 0
	}
	h.Set("Age", strconv.FormatInt(age, 10))
	h.Set("X-Kuso-Stale", "1")
	h.Set("Cache-Control", "no-store")
	h.Del("Expires")
	h.Set("Content-Length", strconv.Itoa(len(e.body)))
	w.WriteHeader(e.status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.body)
	}
}

// staleEpisodeReset forgets a serve-stale wake this old, so an env that
// failed to come up once gets stale serving again on a later visit.
const staleEpisodeReset = 10 * time.Minute

type staleWake struct{ first, lastKick time.Time }

// serveStale answers r from a stored copy when the env is not ready and
// kicks the wake in the background. false means the caller continues
// with the ordinary hold-and-proxy.
func (a *Activator) serveStale(w http.ResponseWriter, r *http.Request, ns, env string) bool {
	if !staleServable(r) {
		return false
	}
	key := ns + "/" + env
	e, ok := a.stale.get(staleKey(key, r), a.now())
	if !ok || !varyMatches(e, r) || a.fastReady(r.Context(), ns, env) {
		return false
	}
	serve, kick := a.claimStaleWake(key)
	if !serve {
		return false
	}
	if kick {
		st, _ := a.startWake(ns, env)
		go func() {
			<-st.done
			if st.err != nil {
				a.logger.Warn("activator: wake behind stale response", "ns", ns, "env", env, "err", st.err)
			}
		}()
	}
	// The same stamp a held request makes once it proxies, so scaledown
	// sees the visit and doesn't re-sleep the env it just woke.
	a.recordActivity(r.Context(), ns, env)
	writeStale(w, r, e, a.now())
	return true
}

// claimStaleWake decides for a stale-eligible request on an env that
// isn't ready: kick a wake at most once per kickWakeInterval (each wake
// rewrites the env CR), and stop serving stale once the wake has taken
// longer than a held request would wait, so a broken deploy surfaces
// through the hold path instead of hiding behind old pages. markReady
// ends the episode.
func (a *Activator) claimStaleWake(key string) (serve, kick bool) {
	now := a.now()
	a.staleMu.Lock()
	defer a.staleMu.Unlock()
	sw, ok := a.staleWakes[key]
	if !ok || now.Sub(sw.first) >= staleEpisodeReset {
		a.staleWakes[key] = staleWake{first: now, lastKick: now}
		return true, true
	}
	if now.Sub(sw.first) >= a.holdTimeout {
		return false, false
	}
	if now.Sub(sw.lastKick) >= kickWakeInterval {
		sw.lastKick = now
		a.staleWakes[key] = sw
		return true, true
	}
	return true, false
}
