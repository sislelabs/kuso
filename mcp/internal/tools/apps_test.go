package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

func newClient(t *testing.T, h http.HandlerFunc) *kusoclient.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return kusoclient.New(&config.Config{URL: srv.URL, Token: "tok"})
}

func TestSummarizeListAppsFiltering(t *testing.T) {
	r := listAppsResult{
		Count: 2,
		Apps: []listAppsItem{
			{Pipeline: "a", Phase: "production", Name: "web", Sleep: "enabled"},
			{Pipeline: "a", Phase: "production", Name: "api"},
		},
	}
	got := summarizeListApps(r, listAppsArgs{Pipeline: "a"})
	if !strings.HasPrefix(got, "2 app(s) in pipeline a.") {
		t.Errorf("summary missing pipeline filter prefix: %q", got)
	}
	if !strings.Contains(got, "[sleeping]") {
		t.Errorf("sleep marker missing: %q", got)
	}
}

func TestRunListAppsFilters(t *testing.T) {
	body := []map[string]any{
		{"name": "web", "pipeline": "a", "phase": "production", "sleep": "disabled", "branch": "main"},
		{"name": "api", "pipeline": "a", "phase": "staging", "sleep": "enabled", "branch": "main"},
		{"name": "site", "pipeline": "b", "phase": "production"},
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(body)
	})

	cases := []struct {
		name string
		args listAppsArgs
		want int
	}{
		{"pipeline filter", listAppsArgs{Pipeline: "a"}, 2},
		{"phase filter", listAppsArgs{Phase: "production"}, 2},
		{"both filters", listAppsArgs{Pipeline: "a", Phase: "production"}, 1},
		{"no filter", listAppsArgs{}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runListApps(context.Background(), client, tc.args)
			if err != nil {
				t.Fatalf("runListApps: %v", err)
			}
			if out.Count != tc.want {
				t.Errorf("Count = %d, want %d", out.Count, tc.want)
			}
		})
	}
}

func TestRunListAppsSortsByPipelineThenPhaseThenName(t *testing.T) {
	body := []map[string]any{
		{"name": "z", "pipeline": "b", "phase": "production"},
		{"name": "a", "pipeline": "a", "phase": "staging"},
		{"name": "b", "pipeline": "a", "phase": "production"},
	}
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	})
	out, err := runListApps(context.Background(), client, listAppsArgs{})
	if err != nil {
		t.Fatalf("runListApps: %v", err)
	}
	want := []string{"a/production/b", "a/staging/a", "b/production/z"}
	for i, w := range want {
		got := out.Apps[i].Pipeline + "/" + out.Apps[i].Phase + "/" + out.Apps[i].Name
		if got != w {
			t.Errorf("Apps[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestRunDescribeAppRequiresAllFields(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server should not be hit; got %s", r.URL.Path)
	})
	if _, err := runDescribeApp(context.Background(), client, describeAppArgs{Pipeline: "a", Phase: "p"}); err == nil {
		t.Fatalf("expected error for missing app, got nil")
	}
}

func TestRunDescribeAppHappyPath(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := "/api/pipelines/p/production/api"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":     "api",
			"pipeline": "p",
			"phase":    "production",
			"sleep":    "disabled",
			"branch":   "main",
			"image":    map[string]any{"repository": "ghcr.io/me/api", "tag": "v1.2"},
			"web":      map[string]any{"replicaCount": 2, "autoscaling": map[string]any{"minReplicas": 1, "maxReplicas": 5, "targetCPUUtilizationPercentage": 80}},
		})
	})

	res, err := runDescribeApp(context.Background(), client, describeAppArgs{Pipeline: "p", Phase: "production", App: "api"})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if res.App.Name != "api" || res.Config.Image.Tag != "v1.2" {
		t.Errorf("unexpected: %+v", res)
	}
}
