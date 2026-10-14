package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"kuso/pkg/kusoApi"
)

// C2: Coolify env vars (DB passwords, API keys) must go through the
// per-key auto path so the server can land them in the managed Secret,
// not the legacy whole-list POST /env that stores CR literals.
func TestImportCoolifyEnvVars_UsesPerKeyAutoPath(t *testing.T) {
	type call struct {
		method, path string
		body         map[string]any
	}
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })

	vars := []struct{ Key, Value string }{{"STRIPE_SECRET_KEY", "sk_live_x"}, {"PORT_NAME", "web"}}
	n, err := importCoolifyEnvVars("shop", "api", vars)
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v", n, err)
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 per-key calls, got %+v", calls)
	}
	for i, c := range calls {
		want := "/api/projects/shop/services/api/env-vars/" + vars[i].Key
		if c.method != http.MethodPut || c.path != want {
			t.Errorf("call %d = %s %s, want PUT %s", i, c.method, c.path, want)
		}
		if c.body["auto"] != true || c.body["value"] != vars[i].Value {
			t.Errorf("call %d body = %v, want {value, auto:true}", i, c.body)
		}
	}
}
