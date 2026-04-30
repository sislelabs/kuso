//go:build integration

// End-to-end MCP integration test.
//
// Builds the kuso-mcp binary, spawns it as a child process pointed at a
// fake kuso server, and exercises every tool via the official MCP SDK
// over stdio. This is the strongest local check we have short of a real
// kuso install — it catches: tool registration regressions, JSON shape
// bugs in args/results, transport wiring, KUSO_URL/KUSO_TOKEN handling,
// and read-only flag plumbing.
//
// Run with:
//
//	go test -tags=integration ./...
//
// Skipped by default (the build tag) so unit-test runs stay fast.

package main_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// startFakeKuso returns a fake kuso server that mirrors the subset of
// endpoints kuso-mcp's tools call.
func startFakeKuso(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "web", "pipeline": "analiz", "phase": "production", "sleep": "disabled", "branch": "main"},
				{"name": "api", "pipeline": "analiz", "phase": "production", "sleep": "enabled", "branch": "main"},
			})

		case strings.HasPrefix(r.URL.Path, "/api/pipelines/analiz/production/api"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "api", "pipeline": "analiz", "phase": "production",
				"sleep": "enabled", "branch": "main",
				"image": map[string]any{"repository": "ghcr.io/sislelabs/example", "tag": "v1.2"},
				"web":   map[string]any{"replicaCount": 2, "autoscaling": map[string]any{"minReplicas": 1, "maxReplicas": 5, "targetCPUUtilizationPercentage": 80}},
			})

		case strings.HasPrefix(r.URL.Path, "/api/apps/analiz/production/api/pods"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "api-7c4b", "phase": "Running", "image": "ghcr.io/sislelabs/example:v1.2"},
			})

		case strings.HasPrefix(r.URL.Path, "/api/logs/analiz/production/api/web/history"):
			_ = json.NewEncoder(w).Encode([]string{"line 1", "line 2", "line 3"})

		case r.URL.Path == "/api/kubernetes/namespace":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"type": "Warning", "reason": "BackOff", "message": "container exited"},
			})

		case strings.HasSuffix(r.URL.Path, "/restart"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))

		default:
			http.Error(w, "unknown path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kuso-mcp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build kuso-mcp: %v\n%s", err, out)
	}
	return bin
}

func newSession(t *testing.T, bin string, kusoURL string, extraArgs ...string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, bin, extraArgs...)
	cmd.Env = append(os.Environ(),
		"KUSO_URL="+kusoURL,
		"KUSO_TOKEN=fake-token",
	)
	transport := &mcp.CommandTransport{Command: cmd}

	client := mcp.NewClient(&mcp.Implementation{Name: "kuso-mcp-test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect to kuso-mcp: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestMCPListTools(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := map[string]bool{
		"health":           true,
		"list_apps":        true,
		"describe_app":     true,
		"troubleshoot_app": true,
		"restart_app":      true,
		"tail_logs":        true,
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("tool %q missing from ListTools; got %v", name, got)
		}
	}
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s returned IsError; content: %+v", name, res.Content)
	}
	return res
}

func TestMCPHealth(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "health", nil)
	if len(res.Content) == 0 {
		t.Fatalf("health returned no content")
	}
}

func TestMCPListAppsRoundTrip(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "list_apps", nil)
	text := contentText(t, res)
	if !strings.Contains(text, "2 app(s) total") {
		t.Errorf("summary missing total count: %q", text)
	}
	if !strings.Contains(text, "[sleeping]") {
		t.Errorf("sleeping marker missing: %q", text)
	}
	if !strings.Contains(text, "analiz/production/api") {
		t.Errorf("analiz/production/api missing: %q", text)
	}
}

func TestMCPDescribeAppRoundTrip(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "describe_app", map[string]any{
		"pipeline": "analiz", "phase": "production", "app": "api",
	})
	text := contentText(t, res)
	if !strings.Contains(text, "analiz/production/api") {
		t.Errorf("path missing: %q", text)
	}
	if !strings.Contains(text, "ghcr.io/sislelabs/example:v1.2") {
		t.Errorf("image missing: %q", text)
	}
	if !strings.Contains(text, "[sleeping]") {
		t.Errorf("sleep marker missing: %q", text)
	}
}

func TestMCPTroubleshootAppAggregates(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "troubleshoot_app", map[string]any{
		"pipeline": "analiz", "phase": "production", "app": "api",
	})
	text := contentText(t, res)
	if !strings.Contains(text, "pods: 1") {
		t.Errorf("pods count missing: %q", text)
	}
	if !strings.Contains(text, "log lines: 3") {
		t.Errorf("log lines count missing: %q", text)
	}
	if !strings.Contains(text, "events: 1") {
		t.Errorf("events count missing: %q", text)
	}
}

func TestMCPRestartAppRequiresConfirm(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "restart_app",
		Arguments: map[string]any{
			"pipeline": "analiz", "phase": "production", "app": "api",
			// confirm omitted
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError when confirm is missing")
	}
}

func TestMCPRestartAppHappyPath(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "restart_app", map[string]any{
		"pipeline": "analiz", "phase": "production", "app": "api",
		"confirm": true,
	})
	text := contentText(t, res)
	if !strings.Contains(text, "restart triggered") {
		t.Errorf("restart confirmation missing: %q", text)
	}
}

func TestMCPReadOnlyRefusesRestart(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL, "--read-only")

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "restart_app",
		Arguments: map[string]any{
			"pipeline": "analiz", "phase": "production", "app": "api",
			"confirm": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError when --read-only and restart called")
	}
}

func TestMCPTailLogsRoundTrip(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "tail_logs", map[string]any{
		"pipeline": "analiz", "phase": "production", "app": "api",
		"lines": 10,
	})
	text := contentText(t, res)
	if !strings.Contains(text, "3 log lines") {
		t.Errorf("line count missing: %q", text)
	}
}

func contentText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}
