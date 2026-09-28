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

type drainReq struct {
	method, path string
	body         map[string]any
}

func drainFakeServer(t *testing.T, testStatus int) *[]drainReq {
	t.Helper()
	var reqs []drainReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		reqs = append(reqs, drainReq{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/drains":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"d1","name":"loki → logs.example.com","type":"loki","url":"https://logs.example.com","enabled":true}`)
		case r.Method == "GET" && r.URL.Path == "/api/drains":
			_, _ = io.WriteString(w, `[{"id":"d1","name":"grafana","type":"otlp","url":"https://otlp.example.com","project":"shop","enabled":true,"headers":{"Authorization":"••••••••"}}]`)
		case strings.HasSuffix(r.URL.Path, "/test"):
			if testStatus >= 300 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":"test send failed: upstream 401: invalid token","upstreamStatus":401}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"status":204}`)
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() {
		api = nil
		drainType, drainURL, drainName, drainProject, drainSecret, drainHeaders, drainDisabled, drainListOutput, drainDeleteYes =
			"", "", "", "", "", nil, false, "table", false
	})
	return &reqs
}

func TestDrainAddSendsBody(t *testing.T) {
	reqs := drainFakeServer(t, 204)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "drain", "add", "--type", "loki", "--url", "https://logs.example.com",
			"--header", "X-Scope-OrgID=tenant-1", "--header", "Authorization=Bearer a=b", "--project", "shop", "--secret", "k"); err != nil {
			t.Fatalf("add: %v", err)
		}
	})
	if len(*reqs) != 1 {
		t.Fatalf("requests: %+v", *reqs)
	}
	b := (*reqs)[0].body
	h, _ := b["headers"].(map[string]any)
	if b["type"] != "loki" || b["url"] != "https://logs.example.com" || b["project"] != "shop" || b["secret"] != "k" ||
		h["X-Scope-OrgID"] != "tenant-1" || h["Authorization"] != "Bearer a=b" {
		t.Fatalf("body = %+v", b)
	}
	if !strings.Contains(out, "d1") {
		t.Fatalf("output should name the new drain id: %s", out)
	}
}

func TestDrainAddRejectsBadHeader(t *testing.T) {
	reqs := drainFakeServer(t, 204)
	if _, err := runRoot(t, "drain", "add", "--type", "http", "--url", "https://x.example.com", "--header", "novalue"); err == nil {
		t.Fatal("want error for --header without '='")
	}
	if len(*reqs) != 0 {
		t.Fatal("must not call the API with a malformed header")
	}
}

func TestDrainListJSON(t *testing.T) {
	drainFakeServer(t, 204)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "drain", "list", "-o", "json"); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 1 {
		t.Fatalf("want a JSON array, got %v:\n%s", err, out)
	}
}

func TestDrainTestSurfacesUpstreamFailure(t *testing.T) {
	drainFakeServer(t, 401)
	_, err := runRoot(t, "drain", "test", "d1")
	if err == nil || !strings.Contains(err.Error(), "invalid token") {
		t.Fatalf("err = %v, want upstream failure surfaced", err)
	}
}

func TestDrainTestOK(t *testing.T) {
	drainFakeServer(t, 204)
	out := captureStdout(t, func() {
		if _, err := runRoot(t, "drain", "test", "d1"); err != nil {
			t.Fatalf("test: %v", err)
		}
	})
	if !strings.Contains(out, "204") {
		t.Fatalf("should print upstream status: %s", out)
	}
}

func TestDrainDelete(t *testing.T) {
	reqs := drainFakeServer(t, 204)
	if _, err := runRoot(t, "drain", "delete", "d1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if r := (*reqs)[0]; r.method != "DELETE" || r.path != "/api/drains/d1" {
		t.Fatalf("got %+v", r)
	}
}
