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

func eventsFakeServer(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	orig := outputFormat
	t.Cleanup(func() { api = nil; outputFormat = orig; getEventsType = "" })
}

// The server answers null for a namespace with no events; `jq '.[]'` breaks.
func TestGetEvents_EmptyIsArray(t *testing.T) {
	eventsFakeServer(t, `null`)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "get", "events", "-o", "json"); err != nil {
			t.Fatalf("get events: %v", err)
		}
	})
	if strings.TrimSpace(out) != "[]" {
		t.Fatalf("want [], got %q", out)
	}
}

func TestGetEvents_TypeFilter(t *testing.T) {
	eventsFakeServer(t, `[{"type":"Normal","reason":"Pulled"},{"type":"Warning","reason":"BackOff"}]`)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "get", "events", "--type", "warning", "-o", "json"); err != nil {
			t.Fatalf("get events: %v", err)
		}
	})
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(got) != 1 || got[0]["reason"] != "BackOff" {
		t.Fatalf("want only the Warning event, got %v", got)
	}
}

func TestDiskUsedCell_NoDataIsDash(t *testing.T) {
	if got := diskUsedCell(0, 0); got != "-" {
		t.Fatalf("no disk data rendered as %q, want -", got)
	}
	if got := diskUsedCell(25, 100); got == "-" {
		t.Fatalf("real disk data rendered as -")
	}
}
