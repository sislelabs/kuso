package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
)

// mountUserPrefs builds a chi router with the UserPrefs routes and a
// middleware that injects the given userID as JWT claims (empty = no
// claims, simulating an unauthenticated request).
func mountUserPrefs(h *httphandlers.UserPrefsHandler, userID string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if userID != "" {
				ctx := auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: userID})
				req = req.WithContext(ctx)
			}
			next.ServeHTTP(w, req)
		})
	})
	h.Mount(r)
	return r
}

func TestUserPrefsHandler_Unauthenticated401(t *testing.T) {
	t.Parallel()
	h := &httphandlers.UserPrefsHandler{} // no DB needed — auth check is first
	srv := mountUserPrefs(h, "")          // no claims injected

	req := httptest.NewRequest(http.MethodGet, "/api/me/project-prefs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET prefs without auth: status %d, want 401", rec.Code)
	}
}

func TestUserPrefsHandler_RoundTrip(t *testing.T) {
	d := openHandlerTestDB(t)
	h := &httphandlers.UserPrefsHandler{DB: d}
	srv := mountUserPrefs(h, "user-1")

	// Star a project.
	body := strings.NewReader(`{"starred":true,"folder":"Clients"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/me/project-prefs/alpha", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT pref: status %d body %s", rec.Code, rec.Body.String())
	}

	// List should show it.
	req = httptest.NewRequest(http.MethodGet, "/api/me/project-prefs", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET prefs: status %d", rec.Code)
	}
	var resp struct {
		Prefs []struct {
			Project string `json:"project"`
			Starred bool   `json:"starred"`
			Folder  string `json:"folder"`
		} `json:"prefs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Prefs) != 1 || resp.Prefs[0].Project != "alpha" ||
		!resp.Prefs[0].Starred || resp.Prefs[0].Folder != "Clients" {
		t.Fatalf("unexpected prefs: %+v", resp.Prefs)
	}

	// Another user must not see user-1's prefs.
	other := mountUserPrefs(h, "user-2")
	req = httptest.NewRequest(http.MethodGet, "/api/me/project-prefs", nil)
	rec = httptest.NewRecorder()
	other.ServeHTTP(rec, req)
	var otherResp struct {
		Prefs []json.RawMessage `json:"prefs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &otherResp)
	if len(otherResp.Prefs) != 0 {
		t.Errorf("user-2 saw %d prefs, want 0 (cross-user leak)", len(otherResp.Prefs))
	}

	// Clear reverts to default (row removed).
	req = httptest.NewRequest(http.MethodDelete, "/api/me/project-prefs/alpha", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE pref: status %d", rec.Code)
	}
	prefs, err := d.ListUserProjectPrefs(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if len(prefs) != 0 {
		t.Errorf("after clear: %d prefs, want 0", len(prefs))
	}
}
