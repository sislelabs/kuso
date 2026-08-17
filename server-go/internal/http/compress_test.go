package http

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// bigJSON is representative of what the dashboard actually polls: a
// repetitive object graph (the /describe payload is project + services +
// envs, each with an embedded CR spec). Repetition is what makes gzip
// worth mounting at all, so the fixture has to be repetitive.
func bigJSON() string {
	var b strings.Builder
	b.WriteString(`{"services":[`)
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"svc","runtime":"dockerfile","port":3000,"replicas":1,"status":"running"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

func TestCompressJSON_CompressesWhenClientAccepts(t *testing.T) {
	body := bigJSON()
	srv := compressJSON()(jsonHandler(body))

	req := httptest.NewRequest(http.MethodGet, "/api/projects/p/describe", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("compressed body (%d B) is not smaller than raw (%d B)", rec.Body.Len(), len(body))
	}

	// The bytes must actually decode back to the exact original — a
	// corrupted or truncated stream would still "look" compressed.
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer zr.Close()
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if string(got) != body {
		t.Errorf("round-tripped body differs from original (%d vs %d bytes)", len(got), len(body))
	}
}

// A client that does not advertise gzip must get plain bytes. The kuso
// CLI and `curl` without --compressed land here.
func TestCompressJSON_PassesThroughWithoutAcceptEncoding(t *testing.T) {
	body := bigJSON()
	srv := compressJSON()(jsonHandler(body))

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty for a non-gzip client", enc)
	}
	if rec.Body.String() != body {
		t.Error("body was altered for a client that did not request gzip")
	}
}

// TestCompressJSON_SkipsStreamingPaths is the regression guard that
// matters most. Wrapping an SSE stream in gzip makes the compressor hold
// bytes until its window fills, so a live log tail shows nothing for a
// long time and looks hung.
func TestCompressJSON_SkipsStreamingPaths(t *testing.T) {
	for _, path := range []string{
		"/ws/projects/p/services/s/logs",
		"/api/projects/p/services/s/logs/stream",
		"/api/projects/p/events/stream",
	} {
		srv := compressJSON()(jsonHandler(bigJSON()))
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if enc := rec.Header().Get("Content-Encoding"); enc == "gzip" {
			t.Errorf("%s: response was gzipped; streaming paths must pass through", path)
		}
	}
}

// hijackableRecorder implements http.Hijacker so we can assert the
// capability survives the middleware.
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// TestCompressJSON_PreservesHijackerOnWebSocketPath guards WebSocket
// upgrades. gorilla/websocket does `w.(http.Hijacker)`; if the compressor
// wrapped the ResponseWriter without proxying Hijack, that assertion
// fails and Upgrade writes a 500 instead of upgrading — which is exactly
// the bug statusRecorder.Hijack was added to fix. Because /ws/ bypasses
// the compressor entirely, the original writer must reach the handler.
func TestCompressJSON_PreservesHijackerOnWebSocketPath(t *testing.T) {
	var sawHijacker bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHijacker = w.(http.Hijacker)
	})

	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/ws/projects/p/services/s/logs", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	compressJSON()(h).ServeHTTP(rec, req)

	if !sawHijacker {
		t.Error("handler on /ws/ path lost http.Hijacker — WebSocket upgrades would 500")
	}
}

// Flush must reach the underlying writer for any streaming handler that
// slips past the path check (a future SSE route on a new prefix).
func TestCompressJSON_PreservesFlusher(t *testing.T) {
	var sawFlusher bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawFlusher = w.(http.Flusher)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	compressJSON()(h).ServeHTTP(httptest.NewRecorder(), req)

	if !sawFlusher {
		t.Error("handler lost http.Flusher through the compressor")
	}
}

func TestIsStreamingPath(t *testing.T) {
	streaming := []string{
		"/ws/projects/p/services/s/logs",
		"/ws/terminal",
		"/api/projects/p/services/s/logs/stream",
		"/api/projects/p/events/stream",
	}
	for _, p := range streaming {
		if !isStreamingPath(p) {
			t.Errorf("isStreamingPath(%q) = false, want true", p)
		}
	}

	discrete := []string{
		"/api/projects",
		"/api/projects/p/describe",
		"/healthz",
		"/api/projects/p/services/s/logs", // non-stream log fetch
	}
	for _, p := range discrete {
		if isStreamingPath(p) {
			t.Errorf("isStreamingPath(%q) = true, want false", p)
		}
	}
}
