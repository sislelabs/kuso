//go:build integration

// End-to-end MCP integration test for the v0.2 tool surface.
//
// Builds kuso-mcp, spawns it as a child process pointed at a fake kuso
// server, and exercises every registered tool via the official MCP SDK
// over stdio. Catches tool-registration regressions, JSON shape bugs,
// confirm-flag enforcement, --read-only refusal, and transport wiring.
//
// Run with:
//   go test -tags=integration ./...

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

// startFakeKuso returns a fake kuso server that mirrors the subset of v0.2
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
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"metadata": map[string]any{"name": "analiz"},
					"spec": map[string]any{
						"defaultRepo": map[string]any{
							"url":           "https://github.com/sislelabs/analiz",
							"defaultBranch": "main",
						},
						"previews": map[string]any{"enabled": true},
					},
				},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/api/projects/analiz":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": map[string]any{
					"metadata": map[string]any{"name": "analiz"},
					"spec": map[string]any{
						"defaultRepo": map[string]any{
							"url":           "https://github.com/sislelabs/analiz",
							"defaultBranch": "main",
						},
					},
				},
				"services":     []any{map[string]any{"metadata": map[string]any{"name": "analiz-api"}}},
				"environments": []any{},
				"addons":       []any{},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/api/projects":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"metadata":{"name":"new"}}`))

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/projects/") && strings.HasSuffix(r.URL.Path, "/services"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"metadata":{"name":"x-y"}}`))

		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/projects/") && strings.HasSuffix(r.URL.Path, "/addons"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"metadata":{"name":"x-pg"}}`))

		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/addons/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))

		default:
			http.Error(w, "unknown path: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
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

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s returned IsError; content: %+v", name, res.Content)
	}
	return res
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

func TestMCPListTools(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	want := map[string]bool{
		"health":            true,
		"list_projects":     true,
		"describe_project":  true,
		"bootstrap_project": true,
		"add_service":       true,
		"manage_addon":      true,
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

func TestMCPListProjects(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "list_projects", nil)
	text := contentText(t, res)
	if !strings.Contains(text, "1 project(s)") {
		t.Errorf("expected count summary, got %q", text)
	}
	if !strings.Contains(text, "analiz") {
		t.Errorf("expected analiz in summary, got %q", text)
	}
	if !strings.Contains(text, "[previews on]") {
		t.Errorf("expected previews marker, got %q", text)
	}
}

func TestMCPDescribeProject(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "describe_project", map[string]any{"project": "analiz"})
	text := contentText(t, res)
	if !strings.Contains(text, "services: 1") {
		t.Errorf("expected services count, got %q", text)
	}
}

func TestMCPBootstrapRequiresConfirm(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bootstrap_project",
		Arguments: map[string]any{
			"name":     "x",
			"repo_url": "https://github.com/x/x",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError when confirm is missing")
	}
}

func TestMCPBootstrapHappyPath(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "bootstrap_project", map[string]any{
		"name":     "newproj",
		"repo_url": "https://github.com/x/x",
		"branch":   "main",
		"confirm":  true,
	})
	text := contentText(t, res)
	if !strings.Contains(text, "newproj created") {
		t.Errorf("expected create confirmation, got %q", text)
	}
}

func TestMCPReadOnlyRefusesBootstrap(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL, "--read-only")

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bootstrap_project",
		Arguments: map[string]any{
			"name":     "x",
			"repo_url": "https://github.com/x/x",
			"confirm":  true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError under --read-only")
	}
}

func TestMCPAddService(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res := callTool(t, s, "add_service", map[string]any{
		"project": "analiz",
		"name":    "api",
		"runtime": "dockerfile",
		"port":    8080,
		"confirm": true,
	})
	text := contentText(t, res)
	if !strings.Contains(text, "analiz/api added") {
		t.Errorf("expected add confirmation, got %q", text)
	}
}

func TestMCPManageAddon(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	add := callTool(t, s, "manage_addon", map[string]any{
		"project": "analiz",
		"action":  "add",
		"name":    "pg",
		"kind":    "postgres",
		"version": "16",
		"confirm": true,
	})
	text := contentText(t, add)
	if !strings.Contains(text, "analiz/pg added") {
		t.Errorf("expected addon add confirmation, got %q", text)
	}

	del := callTool(t, s, "manage_addon", map[string]any{
		"project": "analiz",
		"action":  "delete",
		"name":    "pg",
		"confirm": true,
	})
	if !strings.Contains(contentText(t, del), "analiz/pg deleted") {
		t.Errorf("expected addon delete confirmation, got %q", contentText(t, del))
	}
}

func TestMCPManageAddonRejectsUnknownKind(t *testing.T) {
	srv := startFakeKuso(t)
	bin := buildBinary(t)
	s := newSession(t, bin, srv.URL)

	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manage_addon",
		Arguments: map[string]any{
			"project": "analiz",
			"action":  "add",
			"name":    "x",
			"kind":    "not-a-real-engine",
			"confirm": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError for unsupported kind")
	}
}
