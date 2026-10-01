package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The builds page re-sent every field on save. maxConcurrent was
// `json:"-"`-flagged as unset, so GET returned the default 1, the page
// PUT it back, and every save pinned builds to one at a time. A save
// that omits a field must keep its current value.
func TestSettings_PutBuildKeepsOmittedFields(t *testing.T) {
	r, d, _ := newAdminServer(t)
	seedAdminUser(t, d)
	// openHandlerTestDB doesn't truncate "Setting".
	if _, err := d.ExecContext(context.Background(), `DELETE FROM "Setting" WHERE key LIKE 'build.%'`); err != nil {
		t.Fatal(err)
	}
	tok := loginAndGetToken(t, r)
	do := func(method, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, "/api/admin/settings/build", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rr.Code, rr.Body.String())
		}
		var out map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&out)
		return out
	}

	if got := do(http.MethodGet, ""); got["maxConcurrentSet"] != false {
		t.Fatalf("fresh install maxConcurrentSet = %v, want false", got["maxConcurrentSet"])
	}
	do(http.MethodPut, `{"registryHost":"ghcr.io","registryAuthSecret":"reg"}`)
	got := do(http.MethodPut, `{"memoryLimit":"3Gi"}`)
	if got["maxConcurrentSet"] != false {
		t.Errorf("a save without maxConcurrent pinned it: %+v", got)
	}
	if got["registryHost"] != "ghcr.io" || got["registryAuthSecret"] != "reg" {
		t.Errorf("a save without the registry override wiped it: %+v", got)
	}
	got = do(http.MethodGet, "")
	if got["memoryLimit"] != "3Gi" || got["maxConcurrentSet"] != false || got["registryHost"] != "ghcr.io" {
		t.Errorf("stored settings: %+v", got)
	}
	if got := do(http.MethodPut, `{"maxConcurrent":4}`); got["maxConcurrent"] != float64(4) || got["maxConcurrentSet"] != true {
		t.Errorf("explicit maxConcurrent not applied: %+v", got)
	}
}
