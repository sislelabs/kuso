package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/version"
)

func TestHealthz(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	newRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, rr.Body.String())
	}
	if got := body["status"]; got != "ok" {
		t.Errorf("status: got %q, want ok", got)
	}
	if got, want := body["version"], version.Version(); got != want {
		t.Errorf("version: got %q, want %q", got, want)
	}
}
