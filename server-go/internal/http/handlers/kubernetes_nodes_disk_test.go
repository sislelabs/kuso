package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"kuso/server/internal/kube"
)

// kuso-server is deliberately not granted nodes/proxy, so the per-node
// Summary call 403s. Once seen, it must stop calling every kubelet on
// every uncached request.
func TestFetchNodeMetrics_NodeProxyForbiddenLatchesOff(t *testing.T) {
	nodeProxyForbidden.Store(false)
	t.Cleanup(func() { nodeProxyForbidden.Store(false) })
	var proxyCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/proxy/stats/summary") {
			proxyCalls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"a"},"usage":{"cpu":"1m","memory":"1Ki"}},{"metadata":{"name":"b"},"usage":{"cpu":"1m","memory":"1Ki"}}]}`))
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	kc := &kube.Client{Clientset: cs}
	for i := 0; i < 3; i++ {
		if got := fetchNodeMetrics(context.Background(), kc); len(got) != 2 {
			t.Fatalf("fetch %d: want CPU/mem for 2 nodes, got %v", i, got)
		}
	}
	if n := proxyCalls.Load(); n != 1 {
		t.Fatalf("nodes/proxy called %d times across 3 fetches, want 1", n)
	}
}
