package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRunTroubleshootAppAggregates(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/pipelines/p/production/api"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":     "api",
				"pipeline": "p",
				"phase":    "production",
				"sleep":    "disabled",
				"image":    map[string]any{"repository": "ghcr.io/me/api", "tag": "v1.2"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/apps/p/production/api/pods"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "api-7c4b", "phase": "Running", "image": "ghcr.io/me/api:v1.2"},
			})
		case strings.HasPrefix(r.URL.Path, "/api/logs/p/production/api/web/history"):
			_ = json.NewEncoder(w).Encode([]string{"line 1", "line 2", "line 3"})
		case r.URL.Path == "/api/kubernetes/namespace":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "Warning", "reason": "BackOff", "message": "container exited"},
			})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	})

	out, err := runTroubleshootApp(context.Background(), client, troubleshootArgs{
		Pipeline: "p", Phase: "production", App: "api",
	})
	if err != nil {
		t.Fatalf("runTroubleshootApp: %v", err)
	}
	if out.Spec == nil || out.Spec.Name != "api" {
		t.Errorf("Spec missing or wrong: %+v", out.Spec)
	}
	if len(out.Pods) != 1 {
		t.Errorf("Pods = %d, want 1", len(out.Pods))
	}
	if len(out.Logs) != 3 {
		t.Errorf("Logs = %d, want 3", len(out.Logs))
	}
	if len(out.Events) != 1 {
		t.Errorf("Events = %d, want 1", len(out.Events))
	}
	if len(out.Errors) != 0 {
		t.Errorf("Errors should be empty, got %v", out.Errors)
	}
}

func TestRunTroubleshootAppPartialErrors(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/pipelines/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "api", "pipeline": "p", "phase": "production"})
		default:
			http.Error(w, "boom", http.StatusInternalServerError)
		}
	})

	out, err := runTroubleshootApp(context.Background(), client, troubleshootArgs{
		Pipeline: "p", Phase: "production", App: "api",
	})
	if err != nil {
		t.Fatalf("runTroubleshootApp returned hard error on partial failure: %v", err)
	}
	if out.Spec == nil {
		t.Errorf("Spec should still be populated")
	}
	if len(out.Errors) < 3 {
		t.Errorf("expected at least 3 errors (pods/logs/events), got %d: %v", len(out.Errors), out.Errors)
	}
}

func TestRunTroubleshootAppRequiresAllFields(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := runTroubleshootApp(context.Background(), client, troubleshootArgs{Pipeline: "p"}); err == nil {
		t.Fatalf("expected error for missing fields, got nil")
	}
}
