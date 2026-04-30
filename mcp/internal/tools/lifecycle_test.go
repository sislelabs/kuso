package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

func TestRestartAppRequiresConfirm(t *testing.T) {
	called := false
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) { called = true })

	_, err := runRestartApp(context.Background(), client, restartAppArgs{
		Pipeline: "p", Phase: "production", App: "api",
	})
	if err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("expected confirm error, got %v", err)
	}
	if called {
		t.Errorf("server should not be hit when confirm is false")
	}
}

func TestRestartAppRefusesReadOnly(t *testing.T) {
	c := kusoclient.New(&config.Config{URL: "http://example.invalid", Token: "tok", ReadOnly: true})
	_, err := runRestartApp(context.Background(), c, restartAppArgs{
		Pipeline: "p", Phase: "production", App: "api", Confirm: true,
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only refusal, got %v", err)
	}
}

func TestRestartAppHappyPath(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/apps/p/production/api/restart"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		w.WriteHeader(http.StatusOK)
	})

	out, err := runRestartApp(context.Background(), client, restartAppArgs{
		Pipeline: "p", Phase: "production", App: "api", Confirm: true,
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if out.Status != "restart triggered" {
		t.Errorf("Status = %q, want 'restart triggered'", out.Status)
	}
}

func TestTailLogsDefaults(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/logs/p/production/api/web/history"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q (default container should be 'web')", r.URL.Path, want)
		}
		_ = json.NewEncoder(w).Encode([]string{"a", "b", "c"})
	})

	out, err := runTailLogs(context.Background(), client, tailLogsArgs{
		Pipeline: "p", Phase: "production", App: "api",
	})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if out.Container != "web" {
		t.Errorf("Container = %q, want web", out.Container)
	}
	if len(out.Lines) != 3 {
		t.Errorf("Lines = %d, want 3", len(out.Lines))
	}
}

func TestTailLogsTruncatesOldLines(t *testing.T) {
	logs := make([]string, 500)
	for i := range logs {
		logs[i] = "line"
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(logs)
	})

	out, err := runTailLogs(context.Background(), client, tailLogsArgs{
		Pipeline: "p", Phase: "production", App: "api", Lines: 100,
	})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(out.Lines) != 100 {
		t.Errorf("Lines = %d, want 100 (last-N)", len(out.Lines))
	}
}

func TestTailLogsCustomContainer(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/logs/p/production/api/kuso-build/history"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_ = json.NewEncoder(w).Encode([]string{"build log"})
	})

	out, err := runTailLogs(context.Background(), client, tailLogsArgs{
		Pipeline: "p", Phase: "production", App: "api", Container: "kuso-build",
	})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if out.Container != "kuso-build" {
		t.Errorf("Container = %q", out.Container)
	}
}
