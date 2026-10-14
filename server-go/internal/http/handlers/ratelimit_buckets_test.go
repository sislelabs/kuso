package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// memLimiter swaps allowAttemptFn for an in-memory counter so bucket
// keying is testable without Postgres.
func memLimiter(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	counts := map[string]int{}
	prev := allowAttemptFn
	allowAttemptFn = func(_ context.Context, key string, max int) (bool, time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		counts[key]++
		return counts[key] <= max, time.Second
	}
	t.Cleanup(func() { allowAttemptFn = prev })
}

func okHandler(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func loginReq(remote, xff, user string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"`+user+`","password":"x"}`))
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

// AH-1: behind Traefik with KUSO_TRUSTED_PROXIES unset, the pod-CIDR
// default must let XFF through so distinct clients get distinct buckets.
func TestRateLimit_PodCIDRDefaultSeparatesClientsBehindIngress(t *testing.T) {
	t.Setenv("KUSO_TRUSTED_PROXIES", "")
	memLimiter(t)
	cs := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: corev1.NodeSpec{PodCIDRs: []string{"10.42.0.0/24"}}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: corev1.NodeSpec{PodCIDR: "10.42.1.0/24"}},
	)
	cidrs, err := podCIDRsFromNodes(context.Background(), cs)
	if err != nil || len(cidrs) != 2 {
		t.Fatalf("podCIDRsFromNodes = %v, %v", cidrs, err)
	}
	setDefaultTrustedProxies(cidrs)
	t.Cleanup(func() { setDefaultTrustedProxies(nil) })

	traefik := "10.42.1.9:5555"
	h := RateLimitedLogin(okHandler)
	for i := 0; i < rateLimitMax+2; i++ {
		rr := httptest.NewRecorder()
		h(rr, loginReq(traefik, "203.0.113.66", "admin"))
	}
	rr := httptest.NewRecorder()
	h(rr, loginReq(traefik, "198.51.100.7", "admin"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("different client behind the ingress got %d, want 204", rr.Code)
	}

	// Explicit env still wins: ingress IP not listed → XFF ignored.
	t.Setenv("KUSO_TRUSTED_PROXIES", "192.0.2.1")
	if got := clientIP(loginReq(traefik, "198.51.100.7", "admin")); got != "10.42.1.9" {
		t.Fatalf("explicit KUSO_TRUSTED_PROXIES ignored: clientIP = %q", got)
	}
}

// AH-1: one source hammering one username must not lock out other
// usernames, and the other public surfaces have their own buckets.
func TestRateLimit_LoginBucketPerUsernameAndScopesIsolated(t *testing.T) {
	t.Setenv("KUSO_TRUSTED_PROXIES", "")
	memLimiter(t)
	h := RateLimitedLogin(okHandler)
	for i := 0; i < rateLimitMax+2; i++ {
		h(httptest.NewRecorder(), loginReq("203.0.113.7:1", "", "admin"))
	}
	rr := httptest.NewRecorder()
	h(rr, loginReq("203.0.113.7:1", "", "admin"))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("hammered username got %d, want 429", rr.Code)
	}
	rr = httptest.NewRecorder()
	h(rr, loginReq("203.0.113.7:1", "", "alice"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("other username from same IP got %d, want 204", rr.Code)
	}

	review := RateLimitedReview(okHandler)
	for i := 0; i < rateLimitMax+2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/reviews/x", nil)
		req.RemoteAddr = "198.51.100.9:1"
		review(httptest.NewRecorder(), req)
	}
	invite := RateLimitedInvite(okHandler)
	req := httptest.NewRequest(http.MethodGet, "/api/invites/lookup/x", nil)
	req.RemoteAddr = "198.51.100.9:1"
	rr = httptest.NewRecorder()
	invite(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("invite after review exhaustion got %d, want 204", rr.Code)
	}
}

// Per-IP login cap still bounds username spraying from one source.
func TestRateLimit_LoginPerIPCap(t *testing.T) {
	t.Setenv("KUSO_TRUSTED_PROXIES", "")
	memLimiter(t)
	h := RateLimitedLogin(okHandler)
	last := 0
	for i := 0; i < loginPerIPMax+1; i++ {
		rr := httptest.NewRecorder()
		h(rr, loginReq("203.0.113.7:1", "", "user"+itoaShort(i+1)))
		last = rr.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("spray past per-IP cap got %d, want 429", last)
	}
}

// The handler must still see the full body after the limiter peeked it.
func TestRateLimit_LoginBodyRestored(t *testing.T) {
	memLimiter(t)
	var got string
	h := RateLimitedLogin(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	})
	h(httptest.NewRecorder(), loginReq("203.0.113.7:1", "", "admin"))
	if !strings.Contains(got, `"password":"x"`) {
		t.Fatalf("handler body = %q", got)
	}
}
