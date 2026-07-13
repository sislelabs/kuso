package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/version"
)

// healthz must remain unauthenticated and stable in shape — Phase 0
// shipped {status, version}; the Vue client + uptime probes depend on
// it.
func TestHealthz_Unauthenticated(t *testing.T) {
	t.Parallel()
	iss, err := auth.NewIssuer("test-secret-irrelevant-for-this-route", 0)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" || body["version"] != version.Version() {
		t.Errorf("body: %+v", body)
	}
}

func TestSession_RequiresBearer(t *testing.T) {
	t.Parallel()
	iss, _ := auth.NewIssuer("test-secret", 0)
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// TestMaxBodyBytes_ImportRouteExempt — the global 1 MiB body cap must
// not apply to /api/projects/import, whose documented cap is 16 MiB
// (httphandlers.MaxImportRequestBytes). Everything else keeps 1 MiB.
func TestMaxBodyBytes_ImportRouteExempt(t *testing.T) {
	t.Parallel()
	mw := maxBodyBytes(1 << 20)
	drain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := mw(drain)

	body := func(n int) *bytes.Reader { return bytes.NewReader(make([]byte, n)) }

	// 2 MiB on a normal route → capped.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects", body(2<<20)))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("normal route 2 MiB: status = %d, want 413", rr.Code)
	}

	// 2 MiB on the import route → allowed.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects/import", body(2<<20)))
	if rr.Code != http.StatusOK {
		t.Errorf("import route 2 MiB: status = %d, want 200", rr.Code)
	}

	// Over 16 MiB on the import route → still capped.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects/import", body(int(httphandlers.MaxImportRequestBytes)+1)))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("import route 16 MiB+1: status = %d, want 413", rr.Code)
	}
}
