package activator

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// RUN-1: every held request used to run its own 250ms LIST loop, so 50
// held requests on one wedged env were ~200 LISTs/s. They now share one
// poller per env.
func TestWaitReady_HeldRequestsShareOnePoller(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	var lists, podLists atomic.Int64
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		podLists.Add(1)
		time.Sleep(50 * time.Millisecond)
		return false, nil, nil
	})
	a := New(&kube.Client{Clientset: cs}, slog.Default())
	a.pollInterval = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.waitReady(ctx, "kuso", "app-web-staging")
		}()
	}
	wg.Wait()
	// One poller at 50ms over 500ms is ~10 LISTs; per-request polling
	// would be ~500.
	if n := lists.Load(); n > 25 {
		t.Fatalf("%d endpointslice LISTs for 50 held requests, want one shared poller (<=25)", n)
	}
	// The pod check is cached 2s per env; only a single poller keeps it
	// to one LIST instead of one per held request.
	if n := podLists.Load(); n > 2 {
		t.Fatalf("%d pod LISTs for 50 held requests, want 1", n)
	}
	a.pollMu.Lock()
	left := len(a.polls)
	a.pollMu.Unlock()
	if left != 0 {
		t.Errorf("%d pollers left running after every waiter gave up", left)
	}
}

// RUN-1: on a slow apiserver every request past readyTTL used to issue
// its own LIST.
func TestFastReady_CoalescesConcurrentLists(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	var lists atomic.Int64
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		time.Sleep(200 * time.Millisecond)
		return false, nil, nil
	})
	a := New(&kube.Client{Clientset: cs}, slog.Default())
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.fastReady(context.Background(), "kuso", "app-web-staging")
		}()
	}
	wg.Wait()
	if n := lists.Load(); n > 2 {
		t.Fatalf("%d LISTs for 50 concurrent requests, want them coalesced", n)
	}
}

// RUN-7: an apiserver error must not read as "asleep" for an env this
// replica never saw (fresh activator replica, or idle > 15 min).
func TestFastReady_ListErrorProxiesOptimistically(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "endpointslices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	a := New(&kube.Client{Clientset: cs}, slog.Default())
	if !a.fastReady(context.Background(), "kuso", "app-web-staging") {
		t.Fatal("fastReady = false on a LIST error; an awake app would get a 503 during an apiserver outage")
	}
}

// RUN-5: an app that accepts and never answers must not pin unbounded
// requests in the shared proxy.
func TestBeginProxy_CapsOpenRequests(t *testing.T) {
	t.Parallel()
	a := New(nil, slog.Default())
	a.maxProxyPerEnv, a.maxProxy = 2, 3
	var dones []func()
	for range 2 {
		d, ok := a.beginProxy("kuso", "hung")
		if !ok {
			t.Fatal("request under the per-env cap refused")
		}
		dones = append(dones, d)
	}
	if _, ok := a.beginProxy("kuso", "hung"); ok {
		t.Fatal("per-env cap not enforced")
	}
	d, ok := a.beginProxy("kuso", "other")
	if !ok {
		t.Fatal("another env refused while under the process cap")
	}
	if _, ok := a.beginProxy("kuso", "third"); ok {
		t.Fatal("process-wide cap not enforced")
	}
	d()
	for _, d := range dones {
		d()
	}
	if a.InFlight() != 0 {
		t.Fatalf("InFlight = %d after all closed", a.InFlight())
	}
}

func TestProxyTransport_ResponseHeaderTimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	_, err := newProxyTransport(time.Second, 100*time.Millisecond).RoundTrip(req)
	if !isHeaderTimeout(err) {
		t.Fatalf("err = %v, want a response-header timeout", err)
	}
}

// RUN-4: every env CR write makes the helm-operator reconcile. A stamp
// another activator replica wrote within the interval must not be
// rewritten.
func TestRecordActivity_SkipsWhenAnotherReplicaStamped(t *testing.T) {
	t.Parallel()
	const ns, name = "kuso", "app-web-staging"
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	if err := dyn.Tracker().Create(kube.GVREnvironments, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{"name": name, "namespace": ns,
			"annotations": map[string]any{scaledown.LastActivityAnnotation: fresh}},
	}}, ns); err != nil {
		t.Fatal(err)
	}
	var updates atomic.Int64
	dyn.PrependReactor("update", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		updates.Add(1)
		return false, nil, nil
	})
	a := New(&kube.Client{Dynamic: dyn}, slog.Default())
	a.nowFn = func() time.Time { return now }
	a.recordActivity(context.Background(), ns, name)
	time.Sleep(200 * time.Millisecond)
	if n := updates.Load(); n != 0 {
		t.Fatalf("%d env CR writes for a stamp another replica wrote 1m ago", n)
	}

	// Once that stamp is a full interval old, this replica does write.
	now = now.Add(activityStampInterval)
	a.recordActivity(context.Background(), ns, name)
	deadline := time.Now().Add(3 * time.Second)
	for updates.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stale stamp never refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
