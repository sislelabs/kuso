package activator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// serverBody behaves like an http.Server request body: once closed, reads
// fail. The real transport closes the body on a failed dial.
type serverBody struct {
	r      io.Reader
	closed atomic.Bool
}

func (b *serverBody) Read(p []byte) (int, error) {
	if b.closed.Load() {
		return 0, errors.New("http: invalid Read on closed Body")
	}
	return b.r.Read(p)
}

func (b *serverBody) Close() error { b.closed.Store(true); return nil }

// Against the real http.Transport: a cold-start dial race is retried with
// the client's body streamed, not buffered (no size cap), and the app
// receives it intact exactly once.
func TestRetryTransport_StreamsLargeBodyAcrossDialRetries(t *testing.T) {
	var got atomic.Int64
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		got.Add(n)
	}))
	defer app.Close()
	appAddr := strings.TrimPrefix(app.URL, "http://")
	refused := closedAddr(t)

	var dials atomic.Int32
	d := &net.Dialer{Timeout: time.Second}
	base := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		if dials.Add(1) < 3 {
			return d.DialContext(ctx, network, refused)
		}
		return d.DialContext(ctx, network, appAddr)
	}}
	defer base.CloseIdleConnections()
	rt := &retryTransport{base: base, wait: time.Millisecond, budget: 5 * time.Second}

	const size = 3 << 20
	req, _ := http.NewRequest("POST", "http://app/upload", &serverBody{r: strings.NewReader(strings.Repeat("x", size))})
	req.GetBody = nil
	req.ContentLength = size
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if dials.Load() != 3 {
		t.Errorf("dials = %d, want 3", dials.Load())
	}
	if got.Load() != size {
		t.Errorf("app received %d bytes, want %d", got.Load(), size)
	}
}

// The ready cache can be stale while scaledown zeroes the env. The first
// refused dial must re-trigger the wake (once), or the retry loop waits on
// a 0-endpoint Service that nothing scales back up.
func TestRetryTransport_RefusedDialFiresWakeHookOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	rt := &retryTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls < 4 {
			return nil, errors.New("dial tcp 10.0.0.1:80: connect: connection refused")
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), wait: time.Millisecond}
	var hooks atomic.Int32
	ctx := withDialRetryHook(context.Background(), func() { hooks.Add(1) })
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://x/", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if hooks.Load() != 1 {
		t.Errorf("wake hook fired %d times, want 1", hooks.Load())
	}
}

// A WebSocket/SSE stream stamps activity once at its start. While it stays
// open the activator must keep stamping, or scaledown sleeps the env under it.
func TestStampOpen_RestampsEnvsWithOpenConnections(t *testing.T) {
	const ns, name = "kuso", "app-web-staging"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatal(err)
	}
	kc := &kube.Client{Dynamic: dyn}
	a := New(kc, slog.Default())
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	a.nowFn = func() time.Time { return now }

	stamp := func() string {
		e, err := kc.GetKusoEnvironment(context.Background(), ns, name)
		if err != nil {
			t.Fatal(err)
		}
		return e.Annotations[scaledown.LastActivityAnnotation]
	}
	waitStamp := func(want string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for stamp() != want {
			if time.Now().After(deadline) {
				t.Fatalf("last-activity = %q, want %q", stamp(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	done, _ := a.beginProxy(ns, name)
	a.stampOpen(context.Background())
	waitStamp(now.Format(time.RFC3339))

	// 40 minutes into the same open stream: still stamped.
	now = now.Add(40 * time.Minute)
	a.stampOpen(context.Background())
	waitStamp(now.Format(time.RFC3339))

	done()
	if a.InFlight() != 0 {
		t.Fatalf("InFlight = %d after close, want 0", a.InFlight())
	}
	prev := stamp()
	now = now.Add(40 * time.Minute)
	a.stampOpen(context.Background())
	time.Sleep(100 * time.Millisecond)
	if got := stamp(); got != prev {
		t.Errorf("closed connection still stamped activity: %q", got)
	}
}

// SIGTERM used to make ListenAndServe return at once and the process exit,
// cutting every held and proxied request. Serve must return only after an
// in-flight request completes.
func TestServe_DrainsInFlightRequestOnShutdown(t *testing.T) {
	a := New(nil, slog.Default())
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		done, _ := a.beginProxy("kuso", "app")
		defer done()
		close(started)
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("held-and-delivered"))
	})
	addr := closedAddr(t)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- a.Serve(ctx, srv, DrainConfig{Delay: 50 * time.Millisecond, Budget: 5 * time.Second}, slog.Default())
	}()

	var body string
	var reqErr error
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		var resp *http.Response
		for i := 0; i < 100; i++ {
			resp, reqErr = http.Get("http://" + addr + "/")
			if reqErr == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if reqErr != nil {
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = string(b)
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached the handler")
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-reqDone:
	default:
		t.Fatal("Serve returned before the in-flight request finished")
	}
	if reqErr != nil || body != "held-and-delivered" {
		t.Fatalf("in-flight request cut: body=%q err=%v", body, reqErr)
	}
}

func TestResolveDrainConfig(t *testing.T) {
	t.Setenv("KUSO_ACTIVATOR_DRAIN_SECONDS", "")
	if got := ResolveDrainConfig().Budget; got != 20*time.Second {
		t.Errorf("default budget = %v, want 20s (fits the default 30s grace)", got)
	}
	t.Setenv("KUSO_ACTIVATOR_DRAIN_SECONDS", "65")
	if got := ResolveDrainConfig().Budget; got != 65*time.Second {
		t.Errorf("budget = %v, want 65s", got)
	}
	t.Setenv("KUSO_ACTIVATOR_DRAIN_SECONDS", "nope")
	if got := ResolveDrainConfig().Budget; got != 20*time.Second {
		t.Errorf("bad value budget = %v, want default", got)
	}
}
