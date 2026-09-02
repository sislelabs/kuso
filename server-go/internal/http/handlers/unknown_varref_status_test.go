package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/server/internal/projects"
)

// A ${{ }} ref naming something that doesn't resolve is a client mistake — the
// user typed a name we can't find. It was falling through to the 500 default,
// so the CLI printed "server returned 500: internal" while the actual reason
// ("looked up \"db-staging\"") stayed in the server log, invisible to the
// person who made the typo.
func TestFail_UnknownVarRefIsClientError(t *testing.T) {
	h := &ProjectsHandler{Logger: slog.Default()}
	rec := httptest.NewRecorder()

	err := fmt.Errorf("env var %q: %w (looked up %q)",
		"DATABASE_URL", projects.ErrUnknownVarRef, "db-staging")
	h.fail(rec, "set env-scoped var", err)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d — an unresolvable ref is the caller's error",
			rec.Code, http.StatusBadRequest)
	}
	// The point of the 400 is that the caller learns WHICH name failed.
	if body := rec.Body.String(); !strings.Contains(body, "db-staging") {
		t.Errorf("body = %q, want it to name the ref that failed", body)
	}
}

// Genuine internal failures must still be 500 with an opaque body.
func TestFail_UnknownErrorStaysInternal(t *testing.T) {
	h := &ProjectsHandler{Logger: slog.Default()}
	rec := httptest.NewRecorder()

	h.fail(rec, "set env-scoped var", fmt.Errorf("connection reset by peer"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if body := rec.Body.String(); strings.Contains(body, "connection reset") {
		t.Errorf("body = %q leaked an internal error to the client", body)
	}
}

