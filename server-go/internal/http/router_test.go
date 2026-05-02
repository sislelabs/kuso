package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
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
