package nodemetrics

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"kuso/server/internal/kube"
)

// kuso-server is not granted nodes/proxy, so the Summary call 403s on a
// real cluster. That must surface in the log, not vanish behind a
// bare `continue` (a week of blank disk metrics went unnoticed).
func TestDiskStats_ForbiddenIsLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"cannot get resource \"nodes/proxy\"","code":403}`))
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := &Sampler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(&buf, nil))}

	got := s.diskStats(context.Background(), []string{"server2", "kuso-worker"})
	if len(got) != 0 {
		t.Fatalf("want no disk figures on 403, got %v", got)
	}
	out := buf.String()
	if strings.Count(out, "level=WARN") != 1 {
		t.Fatalf("want exactly one WARN per tick, got:\n%s", out)
	}
	if !strings.Contains(out, "failedNodes=2") || !strings.Contains(out, "nodes/proxy") {
		t.Fatalf("WARN must carry the failure count and the forbidden error, got:\n%s", out)
	}
}

// A 403 is permanent for the life of the process (the grant is withheld
// on purpose), so later ticks must not keep calling every kubelet or
// re-log the same WARN.
func TestDiskStats_ForbiddenLatchesOff(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	s := &Sampler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(&buf, nil))}

	s.diskStats(context.Background(), []string{"a", "b"})
	first := calls
	s.diskStats(context.Background(), []string{"a", "b"})
	if calls != first {
		t.Fatalf("second tick made %d more kubelet calls after a 403", calls-first)
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("want one WARN total, got %d:\n%s", n, buf.String())
	}
}
