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
	registerSubscribeAddon(server, client)
	registerLogs(server, client)
	registerBuild(server, client)
	registerDB(server, client)
	registerStatus(server, client)
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
	// Without auto the server takes the legacy literal path, which stores a
	// ${{ addon.KEY }} ref verbatim and writes secrets as CR literals.
	if body["auto"] != true {
		t.Fatalf("upsert must use the unified auto write, got body %q", got[0].Body)
	}
	if got[1].Method != http.MethodDelete || got[1].Path != "/api/projects/shop/services/api/env-vars/OLD_FLAG" {
		t.Fatalf("unexpected unset request: %+v", got[1])
	}
}

// subscribe_addon is a read-modify-write of the whole list: the PUT must keep
// existing subscriptions, add the new one, and drop the unsubscribed one.
func TestSubscribeAddon_MergesIntoCurrentList(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodGet {
			return http.StatusOK, `{"subscribed":["cache","queue"],"available":["cache","queue","db"]}`
		}
		return http.StatusOK, "{}"
	})
	text, isErr, err := callText(t, sess, "subscribe_addon", map[string]any{
		"project": "shop", "service": "api", "confirm": true,
		"subscribe": []string{"db"}, "unsubscribe": []string{"queue"},
	})
	if err != nil || isErr {
		t.Fatalf("subscribe_addon failed: err=%v text=%s", err, text)
	}
	got := reqs()
	if len(got) != 2 || got[1].Method != http.MethodPut || got[1].Path != "/api/projects/shop/services/api/subscribed-addons" {
		t.Fatalf("want GET then PUT subscribed-addons, got %+v", got)
	}
	var body struct {
		Addons []string `json:"addons"`
	}
	if err := json.Unmarshal([]byte(got[1].Body), &body); err != nil {
		t.Fatalf("bad PUT body %q: %v", got[1].Body, err)
	}
	if strings.Join(body.Addons, ",") != "cache,db" {
		t.Fatalf("want addons [cache db], got %v", body.Addons)
	}

	text, isErr, _ = callText(t, sess, "subscribe_addon", map[string]any{
		"project": "shop", "service": "api", "confirm": true, "subscribe": []string{"typo"},
	})
	if !isErr || !strings.Contains(text, "not in project") {
		t.Fatalf("unknown addon should be refused, got isErr=%v text=%s", isErr, text)
	}
}

// A crash-looping env reads phase=deploying replicas=0/1 forever; only the
// server's state/stateDetail say it's crashing.
func TestStatus_SurfacesStateAndDetail(t *testing.T) {
	sess, _ := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `{"project":{"metadata":{"name":"shop"}},"environments":[{"spec":{"service":"shop-api","kind":"production"},` +
			`"status":{"phase":"deploying","replicas":{"ready":0,"desired":1},"state":"crashlooping","stateDetail":"container \"api\" in CrashLoopBackOff (7 restarts)"}}]}`
	})
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"project": "shop"}})
	if err != nil || res.IsError {
		t.Fatalf("status failed: err=%v res=%+v", err, res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "state=crashlooping") || !strings.Contains(text, "CrashLoopBackOff (7 restarts)") {
		t.Fatalf("status text lost state/stateDetail:\n%s", text)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(raw), `"state":"crashlooping"`) {
		t.Fatalf("structured output missing state: %s", raw)
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

// list_builds used to cut the server's list to `limit` without saying so,
// so an agent hunting for a rollback target couldn't tell older ones existed.
func TestListBuilds_SaysWhenOlderBuildsExist(t *testing.T) {
	sess, _ := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, `[{"id":"b3","status":"succeeded"},{"id":"b2","status":"succeeded"},{"id":"b1","status":"failed"}]`
	})
	text, isErr, err := callText(t, sess, "list_builds", map[string]any{"project": "shop", "service": "api", "limit": 2})
	if err != nil || isErr {
		t.Fatalf("list_builds failed: err=%v text=%s", err, text)
	}
	if !strings.Contains(text, "Older builds exist") {
		t.Fatalf("want an older-builds note, got:\n%s", text)
	}
	text, _, _ = callText(t, sess, "list_builds", map[string]any{"project": "shop", "service": "api", "limit": 5})
	if strings.Contains(text, "Older builds exist") {
		t.Fatalf("no note expected when everything fits, got:\n%s", text)
	}
}
