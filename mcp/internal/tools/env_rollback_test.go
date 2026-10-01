package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type recordedReq struct {
	Method, Path, Body string
}

// newRecordingSession wires the real tools to an httptest server that
// records every request and answers with handler.
func newRecordingSession(t *testing.T, handler func(r *http.Request) (int, string)) (*mcp.ClientSession, func() []recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recordedReq{r.Method, r.URL.Path, string(b)})
		mu.Unlock()
		code, body := handler(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	client := kusoclient.New(&config.Config{URL: srv.URL, Token: "test"})
	server := mcp.NewServer(&mcp.Implementation{Name: "kuso-mcp-test", Version: "test"}, nil)
	registerSetEnv(server, client)
	registerRollback(server, client)
	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, func() []recordedReq {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedReq(nil), reqs...)
	}
}

// set_env must touch only the keys it names. The old whole-list POST to
// /env wiped every omitted key, including addon secretKeyRef entries the
// tool couldn't even represent.
func TestSetEnv_PerKeyNeverReplacesWholeList(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, "{}"
	})
	text, isErr, err := callText(t, sess, "set_env", map[string]any{
		"project": "shop", "service": "api", "confirm": true,
		"envVars": []map[string]string{{"name": "LOG_LEVEL", "value": "debug"}},
		"unset":   []string{"OLD_FLAG"},
	})
	if err != nil || isErr {
		t.Fatalf("set_env failed: err=%v text=%s", err, text)
	}
	got := reqs()
	if len(got) != 2 {
		t.Fatalf("want 2 per-key requests, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if strings.HasSuffix(r.Path, "/env") {
			t.Fatalf("set_env hit the whole-list replace route: %+v", r)
		}
	}
	if got[0].Method != http.MethodPut || got[0].Path != "/api/projects/shop/services/api/env-vars/LOG_LEVEL" {
		t.Fatalf("unexpected upsert request: %+v", got[0])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(got[0].Body), &body); err != nil || body["value"] != "debug" {
		t.Fatalf("unexpected upsert body %q", got[0].Body)
	}
	if got[1].Method != http.MethodDelete || got[1].Path != "/api/projects/shop/services/api/env-vars/OLD_FLAG" {
		t.Fatalf("unexpected unset request: %+v", got[1])
	}
}

// The rollback endpoint returns the re-pointed KusoEnvironment CR, not a
// build summary; decoding it as a build left the output empty.
func TestRollback_DecodesEnvironmentResponse(t *testing.T) {
	sess, _ := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"metadata":{"name":"shop-api-production"},"spec":{"image":{"repository":"registry/shop-api","tag":"abc123"}}}`
	})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "rollback", Arguments: map[string]any{
		"project": "shop", "service": "api", "build": "shop-api-1", "confirm": true,
	}})
	if err != nil || res.IsError {
		t.Fatalf("rollback failed: err=%v res=%+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "abc123") || !strings.Contains(text, "shop-api-production") {
		t.Fatalf("rollback output lost the env/image: %s", text)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"imageTag":"abc123"`) {
		t.Fatalf("structured output missing imageTag: %s", raw)
	}
}
