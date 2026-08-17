package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
)

// /api/usage and /api/usage/projects roll up cost across EVERY project,
// and the /projects variant returns the per-project breakdown by name.
// Both shipped ungated, so any authenticated principal — including a
// viewer scoped to a single project — could enumerate every project on
// the instance and its spend. They now require billing:read.
//
// The handlers are registered directly rather than via Mount() because
// Mount no-ops when DB/Cfg are nil; the permission gate runs before any
// DB access, so a nil DB is fine for asserting the 403.
func TestUsage_RejectsWithoutBillingRead(t *testing.T) {
	t.Parallel()
	paths := []struct {
		name string
		path string
	}{
		{"cluster rollup", "/api/usage"},
		{"per-project rollup", "/api/usage/projects"},
	}
	// Deliberately includes a principal with a real-but-insufficient
	// permission: the gate must check billing:read specifically, not
	// merely "is authenticated" or "has any permission".
	principals := []struct {
		name  string
		perms []string
	}{
		{"no permissions", []string{}},
		{"unrelated permission only", []string{string(auth.PermProjectsCreate)}},
	}

	for _, p := range paths {
		for _, pr := range principals {
			t.Run(p.name+"/"+pr.name, func(t *testing.T) {
				t.Parallel()
				h := &httphandlers.UsageHandler{}
				r := chi.NewRouter()
				r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: pr.perms}))
				r.Get("/api/usage", h.Get)
				r.Get("/api/usage/projects", h.GetProjects)

				rr := httptest.NewRecorder()
				r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p.path, nil))
				if rr.Code != http.StatusForbidden {
					t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
				}
			})
		}
	}
}

// Unauthenticated requests must 401 rather than fall through.
func TestUsage_RejectsUnauthenticated(t *testing.T) {
	t.Parallel()
	h := &httphandlers.UsageHandler{}
	r := chi.NewRouter()
	r.Get("/api/usage", h.Get)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

// A billing:read holder must get PAST the permission gate — otherwise
// this fix would have broken the admin usage page rather than secured
// it. With a nil DB the handler panics once it proceeds to the rollup
// query, so reaching that panic IS the proof the gate let us through;
// a 401/403 would return cleanly instead. Recover so the panic is a
// pass signal rather than a test failure.
func TestUsage_AllowsBillingRead(t *testing.T) {
	t.Parallel()
	h := &httphandlers.UsageHandler{}
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{
		UserID:      "admin",
		Permissions: []string{string(auth.PermBillingRead)},
	}))
	r.Get("/api/usage", h.Get)

	rr := httptest.NewRecorder()
	passedGate := false
	func() {
		defer func() {
			if recover() != nil {
				// Reached the DB layer ⇒ the permission gate allowed it.
				passedGate = true
			}
		}()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
		if rr.Code != http.StatusForbidden && rr.Code != http.StatusUnauthorized {
			passedGate = true
		}
	}()
	if !passedGate {
		t.Errorf("billing:read holder was blocked: status=%d body=%s", rr.Code, rr.Body.String())
	}
}
