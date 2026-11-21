package activator

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func TestStaleCapturableRequest(t *testing.T) {
	t.Parallel()
	req := func(method string, h map[string]string) *http.Request {
		r := httptest.NewRequest(method, "http://site.example.com/", nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := map[string]struct {
		r    *http.Request
		want bool
	}{
		"anonymous GET": {req(http.MethodGet, nil), true},
		"HEAD":          {req(http.MethodHead, nil), false},
		"POST":          {req(http.MethodPost, nil), false},
		"cookie":        {req(http.MethodGet, map[string]string{"Cookie": "sid=1"}), false},
		"authorization": {req(http.MethodGet, map[string]string{"Authorization": "Bearer x"}), false},
		"upgrade":       {req(http.MethodGet, map[string]string{"Upgrade": "websocket"}), false},
		"proxy-auth":    {req(http.MethodGet, map[string]string{"Proxy-Authorization": "Basic x"}), false},
		"api key":       {req(http.MethodGet, map[string]string{"X-Api-Key": "k"}), false},
		"token header":  {req(http.MethodGet, map[string]string{"X-Access-Token": "t"}), false},
		"session":       {req(http.MethodGet, map[string]string{"X-Session-Id": "s"}), false},
		"forwarded":     {req(http.MethodGet, map[string]string{"X-Forwarded-For": "1.2.3.4", "Accept": "text/html"}), true},
	}
	for name, c := range cases {
		if got := staleCapturable(c.r); got != c.want {
			t.Errorf("%s: staleCapturable = %v, want %v", name, got, c.want)
		}
	}
	if !staleServable(req(http.MethodHead, nil)) {
		t.Error("anonymous HEAD must be servable from a stored GET")
	}
	if staleServable(req(http.MethodPost, nil)) {
		t.Error("POST must never be served stale")
	}
	if staleServable(req(http.MethodGet, map[string]string{"User-Agent": uptimeUserAgentPrefix + "1"})) {
		t.Error("kuso uptime checks must not be served stale")
	}
}

func TestStaleStorableResponse(t *testing.T) {
	t.Parallel()
	resp := func(status int, h map[string][]string, cl int64) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header(h), ContentLength: cl}
	}
	cases := map[string]struct {
		r    *http.Response
		want bool
	}{
		"no cache-control":         {resp(200, nil, 10), false},
		"max-age=0 only":           {resp(200, map[string][]string{"Cache-Control": {"max-age=0"}}, 10), false},
		"s-maxage":                 {resp(200, map[string][]string{"Cache-Control": {"s-maxage=31536000, stale-while-revalidate"}}, 10), true},
		"plain public":             {resp(200, map[string][]string{"Cache-Control": {"public"}}, 10), true},
		"unknown length":           {resp(200, map[string][]string{"Cache-Control": {"public"}}, -1), true},
		"public max-age":           {resp(200, map[string][]string{"Cache-Control": {"public, max-age=60"}}, 10), true},
		"vary accept-encoding":     {resp(200, map[string][]string{"Cache-Control": {"public"}, "Vary": {"Accept-Encoding"}}, 10), true},
		"404":                      {resp(404, map[string][]string{"Cache-Control": {"public"}}, 10), false},
		"304":                      {resp(304, nil, 0), false},
		"set-cookie":               {resp(200, map[string][]string{"Cache-Control": {"public"}, "Set-Cookie": {"a=b"}}, 10), false},
		"cache-control private":    {resp(200, map[string][]string{"Cache-Control": {"private, max-age=60"}}, 10), false},
		"cache-control no-store":   {resp(200, map[string][]string{"Cache-Control": {"no-store"}}, 10), false},
		"cache-control no-cache":   {resp(200, map[string][]string{"Cache-Control": {"max-age=0", "no-cache=\"Set-Cookie\""}}, 10), false},
		"vary cookie":              {resp(200, map[string][]string{"Cache-Control": {"public"}, "Vary": {"Accept-Encoding, Cookie"}}, 10), false},
		"vary star":                {resp(200, map[string][]string{"Cache-Control": {"public"}, "Vary": {"*"}}, 10), false},
		"content-length over 1MiB": {resp(200, map[string][]string{"Cache-Control": {"public"}}, staleMaxEntry+1), false},
	}
	for name, c := range cases {
		if got := staleStorable(c.r); got != c.want {
			t.Errorf("%s: staleStorable = %v, want %v", name, got, c.want)
		}
	}
}

func TestStaleKeyByEncodingBucket(t *testing.T) {
	t.Parallel()
	req := func(ae ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://Site.example.com:443/p?q=1", nil)
		for _, v := range ae {
			r.Header.Add("Accept-Encoding", v)
		}
		return r
	}
	if staleKey("e", req("gzip, br")) != staleKey("e", req("br", "gzip;q=0.8")) {
		t.Error("the same accepted set in another order must share a key")
	}
	if staleKey("e", req("gzip")) == staleKey("e", req()) {
		t.Error("a gzip client and an identity client must not share a key")
	}
	if staleKey("e", req("gzip;q=0")) != staleKey("e", req()) {
		t.Error("q=0 is a refusal, not an accepted coding")
	}

	c := newStaleCache(1<<20, 1<<20, 1<<20, time.Hour)
	now := time.Now()
	c.put(&staleEntry{key: staleKey("e", req("gzip")), env: "e", status: 200, header: http.Header{"Content-Encoding": {"gzip"}}, body: []byte("gz"), stored: now})
	if _, ok := c.get(staleKey("e", req()), now); ok {
		t.Fatal("a gzip body was found for a client that did not accept gzip")
	}
	if _, ok := c.get(staleKey("e", req("gzip")), now); !ok {
		t.Fatal("gzip client missed its own entry")
	}
}

func TestStaleCacheCapsAndExpiry(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entry := func(env, key string, n int) *staleEntry {
		return &staleEntry{key: key, env: env, status: 200, body: bytes.Repeat([]byte("x"), n), stored: now}
	}
	unit := int64(1000 + staleHeaderOverhead + 2) // body + 2-byte key + overhead

	t.Run("global cap evicts least recently used", func(t *testing.T) {
		c := newStaleCache(3*unit, 10*unit, 1<<20, time.Hour)
		c.put(entry("a", "a1", 1000))
		c.put(entry("b", "b1", 1000))
		c.put(entry("c", "c1", 1000))
		c.get("a1", now) // a1 is now the most recent; b1 the oldest
		c.put(entry("d", "d1", 1000))
		if _, ok := c.get("b1", now); ok {
			t.Error("b1 should have been evicted")
		}
		for _, k := range []string{"a1", "c1", "d1"} {
			if _, ok := c.get(k, now); !ok {
				t.Errorf("%s evicted", k)
			}
		}
		if c.bytes > c.maxBytes {
			t.Errorf("bytes %d over cap %d", c.bytes, c.maxBytes)
		}
	})

	t.Run("per-env cap evicts only that env", func(t *testing.T) {
		c := newStaleCache(10*unit, 2*unit, 1<<20, time.Hour)
		c.put(entry("b", "b1", 1000))
		c.put(entry("a", "a1", 1000))
		c.put(entry("a", "a2", 1000))
		c.put(entry("a", "a3", 1000))
		if _, ok := c.get("a1", now); ok {
			t.Error("a1 should have been evicted by env a's cap")
		}
		if _, ok := c.get("b1", now); !ok {
			t.Error("env a's cap evicted env b's entry")
		}
		if got := c.envs["a"].bytes; got != 2*unit {
			t.Errorf("env a bytes = %d, want %d", got, 2*unit)
		}
	})

	t.Run("entry over per-env cap is not stored", func(t *testing.T) {
		c := newStaleCache(10*unit, unit-1, 1<<20, time.Hour)
		c.put(entry("a", "a1", 1000))
		if len(c.entries) != 0 || c.bytes != 0 {
			t.Errorf("stored an entry larger than the per-env cap")
		}
	})

	t.Run("expired entries are not served", func(t *testing.T) {
		c := newStaleCache(10*unit, 10*unit, 1<<20, time.Hour)
		c.put(entry("a", "a1", 10))
		if _, ok := c.get("a1", now.Add(time.Hour)); ok {
			t.Error("served an entry past its TTL")
		}
		if c.bytes != 0 || len(c.envs) != 0 {
			t.Errorf("expired entry left accounting behind: bytes=%d envs=%d", c.bytes, len(c.envs))
		}
	})
}

func readThrough(t *testing.T, c *staleCache, body []byte, closeEarly bool) []byte {
	t.Helper()
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(bytes.NewReader(body))}
	c.capture(resp, "e", "k", time.Now)
	var got []byte
	if closeEarly {
		got = make([]byte, 10)
		_, _ = io.ReadFull(resp.Body, got)
	} else {
		var err error
		if got, err = io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	_ = resp.Body.Close()
	return got
}

func TestCaptureBody(t *testing.T) {
	t.Parallel()
	t.Run("stores a complete body", func(t *testing.T) {
		c := newStaleCache(4<<20, 4<<20, 4<<20, time.Hour)
		body := []byte("<html>hi</html>")
		if got := readThrough(t, c, body, false); !bytes.Equal(got, body) {
			t.Fatalf("client got %q", got)
		}
		e, ok := c.get("k", time.Now())
		if !ok || !bytes.Equal(e.body, body) {
			t.Fatalf("stored = %v %+v", ok, e)
		}
		if c.inflight.Load() != 0 {
			t.Errorf("inflight = %d after store", c.inflight.Load())
		}
	})
	t.Run("abandons past 1 MiB but streams it all", func(t *testing.T) {
		c := newStaleCache(4<<20, 4<<20, 4<<20, time.Hour)
		body := bytes.Repeat([]byte("y"), staleMaxEntry+1)
		if got := readThrough(t, c, body, false); len(got) != len(body) {
			t.Fatalf("client got %d bytes, want %d", len(got), len(body))
		}
		if _, ok := c.get("k", time.Now()); ok {
			t.Fatal("stored a body over the 1 MiB cap")
		}
		if c.inflight.Load() != 0 {
			t.Errorf("inflight = %d after abandon", c.inflight.Load())
		}
	})
	t.Run("a body cut short is not stored", func(t *testing.T) {
		c := newStaleCache(4<<20, 4<<20, 4<<20, time.Hour)
		readThrough(t, c, bytes.Repeat([]byte("z"), 100), true)
		if _, ok := c.get("k", time.Now()); ok {
			t.Fatal("stored a partial body")
		}
		if c.inflight.Load() != 0 {
			t.Errorf("inflight = %d after early close", c.inflight.Load())
		}
	})
	t.Run("in-flight budget abandons capture", func(t *testing.T) {
		c := newStaleCache(4<<20, 4<<20, 50, time.Hour)
		readThrough(t, c, bytes.Repeat([]byte("z"), 100), false)
		if _, ok := c.get("k", time.Now()); ok {
			t.Fatal("captured past the in-flight budget")
		}
		if c.inflight.Load() != 0 {
			t.Errorf("inflight = %d", c.inflight.Load())
		}
	})
}

// staleFixture is one sleep-routed env behind a fake apiserver whose
// endpoint readiness the test flips, with the proxy pointed at upstream.
type staleFixture struct {
	a        *Activator
	ready    atomic.Bool
	patches  atomic.Int32
	upstream atomic.Int32
}

const staleNS, staleEnv, staleHost = "kuso", "site-web-production", "site.example.com"

func newStaleFixture(t *testing.T, serveStale bool) *staleFixture {
	t.Helper()
	f := &staleFixture{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.upstream.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = io.WriteString(w, "page "+r.URL.Path)
	}))
	t.Cleanup(up.Close)
	upURL, _ := url.Parse(up.URL)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": staleEnv, "namespace": staleNS},
		"spec": map[string]any{
			"host":         staleHost,
			"image":        map[string]any{"repository": "registry/site", "tag": "v1"},
			"replicaCount": int64(0),
			"sleep":        map[string]any{"enabled": true, "serveStale": serveStale},
		},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, staleNS); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		l := &discoveryv1.EndpointSliceList{}
		if f.ready.Load() {
			yes := true
			l.Items = []discoveryv1.EndpointSlice{{
				ObjectMeta: metav1.ObjectMeta{Name: staleEnv + "-x", Namespace: staleNS, Labels: map[string]string{discoveryv1.LabelServiceName: staleEnv}},
				Endpoints:  []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &yes}}},
			}}
		}
		return true, l, nil
	})
	cs.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		f.patches.Add(1)
		return true, nil, nil
	})
	f.a = New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.a.holdTimeout = 300 * time.Millisecond
	f.a.proxyTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = upURL.Scheme, upURL.Host
		return http.DefaultTransport.RoundTrip(r)
	})
	return f
}

func (f *staleFixture) do(method, path string, h map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+staleHost+path, nil)
	for k, v := range h {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.a.Handler().ServeHTTP(rec, r)
	return rec
}

// sleep makes the env look asleep to the activator (no ready endpoint,
// ready cache dropped, activity throttle expired), as after scaledown.
func (f *staleFixture) sleep() {
	f.ready.Store(false)
	f.a.forgetReady(staleNS, staleEnv)
	f.a.activityMu.Lock()
	clear(f.a.lastStamp)
	f.a.activityMu.Unlock()
}

func TestServeStale_AnswersAsleepEnvAndWakesOnce(t *testing.T) {
	t.Parallel()
	f := newStaleFixture(t, true)
	f.ready.Store(true)
	gz := map[string]string{"Accept-Encoding": "gzip"}
	if rec := f.do(http.MethodGet, "/", gz); rec.Code != 200 || rec.Body.String() != "page /" || rec.Header().Get("X-Kuso-Stale") != "" {
		t.Fatalf("awake proxy: %d %q stale=%q", rec.Code, rec.Body.String(), rec.Header().Get("X-Kuso-Stale"))
	}

	f.sleep()
	before := f.upstream.Load()
	start := time.Now()
	rec := f.do(http.MethodGet, "/", gz)
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("stale answer took %v; it must not wait on the wake", took)
	}
	if rec.Code != 200 || rec.Body.String() != "page /" {
		t.Fatalf("stale: %d %q", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]string{"X-Kuso-Stale": "1", "Cache-Control": "no-store", "Age": "0", "Content-Type": "text/html"} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if head := f.do(http.MethodHead, "/", gz); head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("X-Kuso-Stale") != "1" {
		t.Errorf("HEAD: %d body=%d stale=%q", head.Code, head.Body.Len(), head.Header().Get("X-Kuso-Stale"))
	}
	if f.upstream.Load() != before {
		t.Error("a stale answer reached the upstream")
	}

	waitFor(t, "the wake's deployment patch", func() bool { return f.patches.Load() >= 1 })
	wakeDone := func() bool {
		f.a.wakeMu.Lock()
		defer f.a.wakeMu.Unlock()
		return len(f.a.waking) == 0
	}
	waitFor(t, "the wake to finish", wakeDone)
	f.do(http.MethodGet, "/", gz)
	waitFor(t, "any second wake to finish", wakeDone)
	if got := f.patches.Load(); got != 1 {
		t.Errorf("deployment patches = %d, want exactly 1 wake", got)
	}
	f.a.activityMu.Lock()
	_, stamped := f.a.lastStamp[staleNS+"/"+staleEnv]
	f.a.activityMu.Unlock()
	if !stamped {
		t.Error("serving stale did not record activity")
	}

	// A client that didn't send gzip is in another bucket: held, not served.
	if rec := f.do(http.MethodGet, "/", nil); rec.Header().Get("X-Kuso-Stale") != "" {
		t.Error("served a copy stored for a different Accept-Encoding set")
	}
}

func TestServeStale_HeldPathsUnchanged(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		serveStale bool
		method     string
		h          map[string]string
	}{
		"serveStale off": {false, http.MethodGet, nil},
		"POST":           {true, http.MethodPost, nil},
		"cookie":         {true, http.MethodGet, map[string]string{"Cookie": "sid=1"}},
		"cache miss":     {true, http.MethodGet, map[string]string{"X-Miss": "1"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newStaleFixture(t, tc.serveStale)
			f.ready.Store(true)
			f.do(http.MethodGet, "/", nil)
			f.sleep()
			path := "/"
			if tc.h["X-Miss"] != "" {
				path = "/other"
			}
			rec := f.do(tc.method, path, tc.h)
			if rec.Header().Get("X-Kuso-Stale") != "" {
				t.Fatal("served stale")
			}
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want the held path's 503 after the hold budget", rec.Code)
			}
		})
	}
}

func TestServeStale_StopsOnceWakeOutlastsHold(t *testing.T) {
	t.Parallel()
	f := newStaleFixture(t, true)
	f.ready.Store(true)
	f.do(http.MethodGet, "/", nil)
	f.sleep()
	var skew atomic.Int64
	f.a.nowFn = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	if rec := f.do(http.MethodGet, "/", nil); rec.Header().Get("X-Kuso-Stale") != "1" {
		t.Fatal("first request after sleep not served stale")
	}
	skew.Store(int64(f.a.holdTimeout))
	if rec := f.do(http.MethodGet, "/", nil); rec.Header().Get("X-Kuso-Stale") != "" {
		t.Fatal("still serving stale after the wake outlasted the hold budget")
	}
}

func TestSlimEnvKeepsServeStale(t *testing.T) {
	t.Parallel()
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "e", "namespace": "n"},
		"spec":     map[string]any{"host": "h.example.com", "sleep": map[string]any{"serveStale": true}, "envVars": []any{"x"}},
	}}
	out, err := slimEnvForHostIndex(u)
	if err != nil {
		t.Fatal(err)
	}
	var e kube.KusoEnvironment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(out.(*unstructured.Unstructured).Object, &e); err != nil {
		t.Fatal(err)
	}
	if !buildHostIndex([]kube.KusoEnvironment{e}, time.Now()).exact["h.example.com"].serveStale {
		t.Fatal("serveStale lost between the informer transform and the host index")
	}
}
