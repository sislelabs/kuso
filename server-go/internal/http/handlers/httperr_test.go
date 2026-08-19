package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v (body=%q)", err, rr.Body.String())
	}
	return m
}

func TestWriteErrEnvelope(t *testing.T) {
	cases := []struct {
		status   int
		msg      string
		wantCode string
	}{
		{http.StatusBadRequest, "bad request: unexpected EOF", "bad_request"},
		{http.StatusUnauthorized, "unauthorized", "unauthorized"},
		{http.StatusForbidden, "forbidden", "forbidden"},
		{http.StatusNotFound, "service not found", "not_found"},
		{http.StatusConflict, "addon p/x already exists", "conflict"},
		{http.StatusGone, "invite has expired", "gone"},
		{http.StatusTooManyRequests, "rate limited", "rate_limited"},
		{http.StatusInternalServerError, "internal", "internal"},
		{http.StatusBadGateway, "upstream", "internal"},
		{http.StatusTeapot, "teapot", "error"},
	}
	for _, tc := range cases {
		rr := httptest.NewRecorder()
		writeErr(rr, tc.status, tc.msg)
		if rr.Code != tc.status {
			t.Errorf("status %d: got %d", tc.status, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("status %d: content-type = %q", tc.status, ct)
		}
		if ns := rr.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
			t.Errorf("status %d: nosniff header missing", tc.status)
		}
		m := decodeEnvelope(t, rr)
		if m["error"] != tc.msg {
			t.Errorf("status %d: error = %q, want %q", tc.status, m["error"], tc.msg)
		}
		if m["code"] != tc.wantCode {
			t.Errorf("status %d: code = %q, want %q", tc.status, m["code"], tc.wantCode)
		}
	}
}

func TestWriteErrExtraReservedFields(t *testing.T) {
	rr := httptest.NewRecorder()
	writeErrExtra(rr, http.StatusConflict, "shadowed by project scope", "shadowed", map[string]any{
		"key":   "DATABASE_URL",
		"scope": "project",
		// Reserved fields must not be overridable by extras.
		"error": "spoofed",
		"code":  "spoofed",
	})
	m := decodeEnvelope(t, rr)
	if m["error"] != "shadowed by project scope" {
		t.Errorf("error = %q", m["error"])
	}
	if m["code"] != "shadowed" {
		t.Errorf("code = %q", m["code"])
	}
	if m["key"] != "DATABASE_URL" || m["scope"] != "project" {
		t.Errorf("extras missing: %v", m)
	}
}

func TestNotFoundMsg(t *testing.T) {
	sentinel := errors.New("projects: not found")
	if got := notFoundMsg(sentinel, sentinel, "service"); got != "service not found" {
		t.Errorf("bare sentinel: got %q", got)
	}
	wrapped := fmt.Errorf("%w: service p/s", sentinel)
	if got := notFoundMsg(wrapped, sentinel, "service"); got != "projects: not found: service p/s" {
		t.Errorf("wrapped: got %q", got)
	}
	if got := notFoundMsg(sentinel, sentinel, ""); got != "resource not found" {
		t.Errorf("empty kind: got %q", got)
	}
}

func TestKindFromOp(t *testing.T) {
	for op, want := range map[string]string{
		"get service":    "service",
		"delete project": "project",
		"drift":          "drift",
		"":               "resource",
	} {
		if got := kindFromOp(op); got != want {
			t.Errorf("kindFromOp(%q) = %q, want %q", op, got, want)
		}
	}
}
