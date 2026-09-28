package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/server/internal/auth"
)

// TestDuplicateGroupAndRoleName_409 — the unique index on name used to
// surface as 500 "internal"; it must be a 409 naming the duplicate.
func TestDuplicateGroupAndRoleName_409(t *testing.T) {
	r, _, iss := newInviteServer(t)
	tok := mintToken(t, iss, "admin", auth.PermUserWrite)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	for _, path := range []string{"/api/groups", "/api/roles"} {
		if rr := post(path, `{"name":"ops"}`); rr.Code != http.StatusCreated {
			t.Fatalf("%s first create: %d %s", path, rr.Code, rr.Body.String())
		}
		rr := post(path, `{"name":"ops"}`)
		if rr.Code != http.StatusConflict {
			t.Fatalf("%s duplicate: status %d body %s, want 409", path, rr.Code, rr.Body.String())
		}
		var env map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &env)
		if msg, _ := env["error"].(string); !strings.Contains(msg, `"ops" already exists`) {
			t.Errorf("%s duplicate message %q", path, msg)
		}
		rr = post(path, `{"name":`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid JSON body") {
			t.Errorf("%s malformed body: %d %s", path, rr.Code, rr.Body.String())
		}
		rr = post(path, `{}`)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `\"name\" is required`) {
			t.Errorf("%s missing name: %d %s", path, rr.Code, rr.Body.String())
		}
	}
}
