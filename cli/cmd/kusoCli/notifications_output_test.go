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

func notificationsFakeServer(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/n1") {
			_, _ = io.WriteString(w, `{"success":true,"data":{"id":"n1","name":"ops","type":"webhook","config":{"url":"https://hooks.example/abc","secret":"s3cr3t"}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"data":[{"id":"n1","name":"ops","type":"webhook","enabled":true,"events":["build.failed"]}]}`)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	origOut, origGet := outputFormat, notificationsGetOutput
	t.Cleanup(func() { api = nil; outputFormat, notificationsGetOutput = origOut, origGet })
}

func TestNotificationsListHonoursJSON(t *testing.T) {
	notificationsFakeServer(t)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "notifications", "list", "-o", "json"); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 1 {
		t.Fatalf("-o json should print a JSON array, got %v:\n%s", err, out)
	}
}

func TestNotificationsGetDefaultsToRedactedPretty(t *testing.T) {
	notificationsFakeServer(t)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "notifications", "get", "n1", "-o", "pretty"); err != nil {
			t.Fatalf("get: %v", err)
		}
	})
	if strings.Contains(out, "s3cr3t") {
		t.Fatalf("pretty output must redact the secret, got:\n%s", out)
	}
}

func TestNotificationsGetJSONStillRedacts(t *testing.T) {
	notificationsFakeServer(t)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "notifications", "get", "n1", "-o", "json"); err != nil {
			t.Fatalf("get: %v", err)
		}
	})
	var one map[string]any
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatalf("-o json should print JSON: %v\n%s", err, out)
	}
	if strings.Contains(out, "s3cr3t") {
		t.Fatalf("-o json must redact unless --reveal, got:\n%s", out)
	}
}
