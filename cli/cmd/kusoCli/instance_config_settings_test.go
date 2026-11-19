package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

func settingsServer(t *testing.T, current string) *map[string]any {
	t.Helper()
	put := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &put)
		}
		_, _ = io.WriteString(w, current)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil })
	return &put
}

// Echoing the GET back pinned maxConcurrent and turned off the adaptive
// build cap; the build endpoint treats omitted fields as "keep".
func TestBuildSettingsSetSendsOnlyNamedKeys(t *testing.T) {
	put := settingsServer(t, `{"maxConcurrent":1,"maxConcurrentSet":false,"memoryLimit":"2Gi"}`)
	captureStdout(t, func() {
		if err := runSettingsSet("/api/admin/settings/build", []string{"memoryLimit=4Gi"}, true); err != nil {
			t.Fatal(err)
		}
	})
	if len(*put) != 1 || (*put)["memoryLimit"] != "4Gi" {
		t.Fatalf("PUT body %v", *put)
	}
}

func TestSettingsSetRejectsUnknownKey(t *testing.T) {
	put := settingsServer(t, `{"ttlSeconds":3600,"neverExpire":false}`)
	err := runSettingsSet("/api/admin/settings/session", []string{"ttlSecond=60"}, false)
	if err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("want unknown-key error, got %v", err)
	}
	if len(*put) != 0 {
		t.Fatalf("nothing may be written, got %v", *put)
	}
}
