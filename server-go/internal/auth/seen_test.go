package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSeenThrottle_OncePerInterval(t *testing.T) {
	t.Parallel()
	th := NewSeenThrottle(5*time.Minute, 100)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if !th.Allow("u1", t0) {
		t.Fatal("first event must be allowed")
	}
	if th.Allow("u1", t0.Add(4*time.Minute+59*time.Second)) {
		t.Fatal("second event inside the interval must be throttled")
	}
	if !th.Allow("u2", t0.Add(time.Second)) {
		t.Fatal("other users are throttled independently")
	}
	if !th.Allow("u1", t0.Add(5*time.Minute)) {
		t.Fatal("event at the interval boundary must be allowed")
	}
}

func TestSeenThrottle_Bounded(t *testing.T) {
	t.Parallel()
	th := NewSeenThrottle(5*time.Minute, 3)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, k := range []string{"a", "b", "c"} {
		th.Allow(k, t0)
	}
	// All three are still fresh: adding a 4th resets instead of growing.
	if !th.Allow("d", t0.Add(time.Minute)) {
		t.Fatal("new key must be allowed when full")
	}
	if n := th.Len(); n > 3 {
		t.Fatalf("map grew past bound: %d", n)
	}

	// Expired entries are pruned first, keeping fresh ones throttled.
	th2 := NewSeenThrottle(5*time.Minute, 3)
	th2.Allow("old1", t0)
	th2.Allow("old2", t0)
	th2.Allow("fresh", t0.Add(9*time.Minute))
	if !th2.Allow("new", t0.Add(10*time.Minute)) {
		t.Fatal("new key must be allowed")
	}
	if th2.Len() != 2 {
		t.Fatalf("want fresh+new after prune, got %d keys", th2.Len())
	}
	if th2.Allow("fresh", t0.Add(10*time.Minute)) {
		t.Fatal("prune must keep unexpired entries throttled")
	}
}

func TestThrottledSeenRecorder_WritesOncePerUser(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	writes := map[string]int{}
	done := make(chan struct{}, 10)
	rec := NewThrottledSeenRecorder(time.Hour, 100, func(_ context.Context, id string, _ time.Time) error {
		mu.Lock()
		writes[id]++
		mu.Unlock()
		done <- struct{}{}
		return nil
	}, nil)

	rec(&Claims{UserID: "u1"})
	rec(&Claims{UserID: "u1"})
	rec(&Claims{UserID: "u2"})
	rec(&Claims{UserID: ""})
	rec(nil)
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for async write")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if writes["u1"] != 1 || writes["u2"] != 1 || len(writes) != 2 {
		t.Fatalf("writes = %v", writes)
	}
}

func TestMiddleware_SeenRecorderOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	var mu sync.Mutex
	var seen []string
	iss.SetSeenRecorder(func(c *Claims) {
		mu.Lock()
		seen = append(seen, c.UserID)
		mu.Unlock()
	})
	revokedJTI := "revoked-jti"
	iss.SetRevocationChecker(func(_ context.Context, jti, _ string, _ time.Time) bool {
		return jti == revokedJTI
	})
	h := iss.Middleware("/public")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	good, _ := iss.Sign(Claims{UserID: "u1"})
	bad := Claims{UserID: "u2"}
	bad.ID = revokedJTI
	revoked, _ := iss.Sign(bad)

	for _, tc := range []struct {
		path, auth string
		want       int
	}{
		{"/x", "Bearer " + good, http.StatusOK},
		{"/x", "", http.StatusUnauthorized},
		{"/x", "Bearer garbage", http.StatusUnauthorized},
		{"/x", "Bearer " + revoked, http.StatusUnauthorized},
		{"/public", "", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("%s %q: status %d, want %d", tc.path, tc.auth, rr.Code, tc.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "u1" {
		t.Fatalf("seen = %v, want [u1]", seen)
	}
}
