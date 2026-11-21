package activator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestCache() *staleCache { return newStaleCache(64<<20, 8<<20, 16<<20, time.Hour) }

// peerPair wires replica A to push into replica B's peer listener.
func peerPair(t *testing.T, bKnowsLoopback bool) (a, b *staleCache, bPeers *stalePeers, pushes *atomic.Int32) {
	t.Helper()
	a, b = newTestCache(), newTestCache()
	list := func(ips ...string) func(context.Context) ([]string, error) {
		return func(context.Context) ([]string, error) { return ips, nil }
	}
	known := "10.9.9.9"
	if bKnowsLoopback {
		known = "127.0.0.1"
	}
	bPeers = newStalePeers(b, "0", list(known), func(string) bool { return false }, slog.Default())
	pushes = &atomic.Int32{}
	countB := func(*staleEntry) { pushes.Add(1) }
	b.onStore.Store(&countB)

	srv := httptest.NewServer(bPeers)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	aPeers := newStalePeers(a, port, list(host, "10.0.0.1"), func(ip string) bool { return ip == "10.0.0.1" }, slog.Default())
	push := aPeers.push
	a.onStore.Store(&push)
	return a, b, bPeers, pushes
}

func captureInto(t *testing.T, c *staleCache, key, env, body string) {
	t.Helper()
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(body))}
	c.capture(resp, env, key, time.Now)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
}

func TestStalePeers_ReplicateCapturedCopy(t *testing.T) {
	t.Parallel()
	a, b, _, bPushes := peerPair(t, true)
	key := "ns/web\x00site.example.com\x00/\x00gzip"
	captureInto(t, a, key, "ns/web", "page")

	waitFor(t, "the copy to reach the peer", func() bool { _, ok := b.get(key, time.Now()); return ok })
	e, _ := b.get(key, time.Now())
	if string(e.body) != "page" || e.header.Get("Content-Type") != "text/html" || e.env != "ns/web" {
		t.Errorf("peer copy = %q %v env=%q", e.body, e.header, e.env)
	}
	if n := bPushes.Load(); n != 0 {
		t.Errorf("a received copy was pushed on %d times; peers would bounce it forever", n)
	}
}

func TestStalePeers_RejectsUnknownSenderAndBadCopies(t *testing.T) {
	t.Parallel()
	_, b, bPeers, _ := peerPair(t, false)
	post := func(meta staleWire) int {
		raw, _ := json.Marshal(meta)
		r := httptest.NewRequest(http.MethodPost, stalePeerPath, strings.NewReader("x"))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set(stalePeerMetaHdr, base64.StdEncoding.EncodeToString(raw))
		rec := httptest.NewRecorder()
		bPeers.ServeHTTP(rec, r)
		return rec.Code
	}
	good := staleWire{Key: "ns/web\x00h\x00/\x00", Env: "ns/web", Status: 200}
	if code := post(good); code != http.StatusForbidden {
		t.Fatalf("unknown sender: %d, want 403", code)
	}
	bPeers.list = func(context.Context) ([]string, error) { return []string{"127.0.0.1"}, nil }
	bPeers.ips = nil
	if code := post(staleWire{Key: "other/env\x00h\x00/\x00", Env: "ns/web", Status: 200}); code != http.StatusBadRequest {
		t.Errorf("key outside its env: %d, want 400", code)
	}
	if code := post(staleWire{Key: good.Key, Env: good.Env, Status: 500}); code != http.StatusBadRequest {
		t.Errorf("non-200 copy: %d, want 400", code)
	}
	if code := post(good); code != http.StatusNoContent {
		t.Fatalf("known sender: %d, want 204", code)
	}
	if _, ok := b.get(good.Key, time.Now()); !ok {
		t.Error("accepted copy not stored")
	}
}
