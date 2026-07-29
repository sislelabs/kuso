package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

// newTestSession wires a real server (with the real tool surface) to an
// in-memory client. The client is pointed at an unroutable URL so any
// tool that ACTUALLY reaches the HTTP layer fails with a network error —
// which lets us distinguish "refused at the confirm gate" (a clean,
// network-free error message) from "passed the gate and tried to call".
func newTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	cfg := &config.Config{URL: "http://127.0.0.1:1", Token: "test"}
	client := kusoclient.New(cfg)

	server := mcp.NewServer(&mcp.Implementation{Name: "kuso-mcp-test", Version: "test"}, nil)
	registerBuild(server, client)
	registerRun(server, client)
	registerSetSecret(server, client)
	registerSetEnv(server, client)

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	sess, err := mcpClient.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// callText runs a tool and returns the flattened text content + whether
// the result was flagged as an error.
func callText(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (string, bool, error) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError, nil
}

// TestMutatingToolsRequireConfirm asserts run/build/set_secret refuse to
// act without confirm=true. Each is checked twice: without confirm it
// must fail at the gate with a "confirm=true is required" message (and
// NOT a network error, proving it never reached the HTTP layer); with
// confirm=true it must get PAST the gate (surfacing a network error
// against the unroutable test URL) — proving the gate is the only thing
// blocking it, not some unrelated validation.
func TestMutatingToolsRequireConfirm(t *testing.T) {
	sess := newTestSession(t)

	base := map[string]map[string]any{
		"run": {
			"project": "shop", "service": "api", "command": []string{"echo", "hi"},
		},
		"build": {
			"project": "shop", "service": "api",
		},
		"set_secret": {
			"project": "shop", "service": "api", "key": "TOKEN", "value": "x",
		},
	}

	for tool, args := range base {
		t.Run(tool+"/no-confirm-refused", func(t *testing.T) {
			text, isErr, err := callText(t, sess, tool, args)
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			if !isErr {
				t.Fatalf("%s without confirm should be an error result, got ok: %s", tool, text)
			}
			if !strings.Contains(text, "confirm=true is required") {
				t.Fatalf("%s without confirm: want confirm-gate message, got: %s", tool, text)
			}
			if strings.Contains(strings.ToLower(text), "connection refused") ||
				strings.Contains(strings.ToLower(text), "dial") {
				t.Fatalf("%s without confirm reached the HTTP layer (should be gated first): %s", tool, text)
			}
		})

		t.Run(tool+"/confirm-passes-gate", func(t *testing.T) {
			withConfirm := map[string]any{"confirm": true}
			for k, v := range args {
				withConfirm[k] = v
			}
			text, isErr, err := callText(t, sess, tool, withConfirm)
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			// Past the gate it must hit the network (and fail there,
			// since the URL is unroutable) — NOT be refused for confirm.
			if isErr && strings.Contains(text, "confirm=true is required") {
				t.Fatalf("%s WITH confirm was still refused at the gate: %s", tool, text)
			}
		})
	}
}
