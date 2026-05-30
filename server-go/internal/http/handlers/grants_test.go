package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	httpsrv "kuso/server/internal/http"
)

// Grant-management endpoints are admin-only (user:write). These tests
// exercise the auth gate + request validation end-to-end through the
// real router. PG-backed; skip without KUSO_TEST_PG_DSN.

func newGrantsServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d := openHandlerTestDB(t)
	iss, _ := auth.NewIssuer("test-secret", time.Hour)
	r := httpsrv.NewRouter(httpsrv.Deps{DB: d, Issuer: iss, Logger: slog.Default()})
	return r, d, iss
}

func seedGrantsUser(t *testing.T, d *db.DB, id string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES (?, ?, ?, 'h', false, true, 'local', NOW(), NOW())`, id, id, id+"@x"); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func do(t *testing.T, r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestGrants_NoAuth_401(t *testing.T) {
	r, _, _ := newGrantsServer(t)
	for _, rt := range []struct{ m, p string }{
		{http.MethodPut, "/api/users/u1/instance-role"},
		{http.MethodPut, "/api/groups/g1/instance-role"},
		{http.MethodGet, "/api/projects/p1/grants"},
		{http.MethodPost, "/api/projects/p1/grants"},
		{http.MethodDelete, "/api/projects/p1/grants/x"},
	} {
		rr := do(t, r, rt.m, rt.p, "", "")
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: code=%d want 401", rt.m, rt.p, rr.Code)
		}
	}
}

func TestGrants_NonAdmin_403(t *testing.T) {
	r, _, iss := newGrantsServer(t)
	// A token with no instance perms (the v2 non-admin JWT shape).
	tok := mintToken(t, iss, "viewer-user")
	rr := do(t, r, http.MethodPost, "/api/projects/p1/grants", tok, `{"userId":"u1","role":"editor"}`)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-admin add grant: code=%d want 403", rr.Code)
	}
}

func TestGrants_AddListRemove_HappyPath(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedGrantsUser(t, d, "alice")
	admin := mintToken(t, iss, "admin", auth.PermSettingsAdmin, auth.PermUserWrite)

	// Add a direct user grant with an editor override.
	rr := do(t, r, http.MethodPost, "/api/projects/p1/grants", admin, `{"userId":"alice","role":"editor"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("add grant: code=%d body=%s", rr.Code, rr.Body.String())
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &added); err != nil || added.ID == "" {
		t.Fatalf("add grant: bad response %s (err %v)", rr.Body.String(), err)
	}

	// List shows it.
	rr = do(t, r, http.MethodGet, "/api/projects/p1/grants", admin, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list grants: code=%d", rr.Code)
	}
	var listed struct {
		Grants []db.ProjectGrant `json:"grants"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(listed.Grants) != 1 || listed.Grants[0].UserID != "alice" || listed.Grants[0].RoleOverride != db.ProjectRoleEditor {
		t.Fatalf("listed grants = %+v, want one alice/editor grant", listed.Grants)
	}

	// Remove it.
	rr = do(t, r, http.MethodDelete, "/api/projects/p1/grants/"+added.ID, admin, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove grant: code=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = do(t, r, http.MethodGet, "/api/projects/p1/grants", admin, "")
	_ = json.Unmarshal(rr.Body.Bytes(), &listed)
	if len(listed.Grants) != 0 {
		t.Fatalf("after remove, grants = %+v, want empty", listed.Grants)
	}
}

func TestGrants_AddGrant_Validation(t *testing.T) {
	r, _, iss := newGrantsServer(t)
	admin := mintToken(t, iss, "admin", auth.PermSettingsAdmin, auth.PermUserWrite)

	cases := []struct {
		name string
		body string
	}{
		{"neither grantee", `{"role":"editor"}`},
		{"both grantees", `{"userId":"u1","groupId":"g1","role":"editor"}`},
		{"invalid role", `{"userId":"u1","role":"superuser"}`},
	}
	for _, tc := range cases {
		rr := do(t, r, http.MethodPost, "/api/projects/p1/grants", admin, tc.body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d want 400 (body %s)", tc.name, rr.Code, rr.Body.String())
		}
	}
}

func TestGrants_SetUserInstanceRole_Validation(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedGrantsUser(t, d, "bob")
	admin := mintToken(t, iss, "admin", auth.PermSettingsAdmin, auth.PermUserWrite)

	// Valid.
	rr := do(t, r, http.MethodPut, "/api/users/bob/instance-role", admin, `{"role":"editor"}`)
	if rr.Code != http.StatusNoContent {
		t.Errorf("set valid role: code=%d body=%s", rr.Code, rr.Body.String())
	}
	// Invalid role rejected.
	rr = do(t, r, http.MethodPut, "/api/users/bob/instance-role", admin, `{"role":"wizard"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("set invalid role: code=%d want 400", rr.Code)
	}
	// Unknown user → 404.
	rr = do(t, r, http.MethodPut, "/api/users/ghost/instance-role", admin, `{"role":"viewer"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("set role on missing user: code=%d want 404", rr.Code)
	}
}
