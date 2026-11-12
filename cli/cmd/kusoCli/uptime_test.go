package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestUptimeCommandsSendExpectedPatch(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"project":{"spec":{}}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(body)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"uptime", "disable", "shop"}, `PATCH /api/projects/shop {"uptime":{"disabled":true}}`},
		{[]string{"uptime", "enable", "shop"}, `PATCH /api/projects/shop {"uptime":{"disabled":false}}`},
		{[]string{"uptime", "disable", "shop", "web"}, `PATCH /api/projects/shop/services/web {"uptime":{"disabled":true}}`},
		{[]string{"uptime", "enable", "shop", "web"}, `PATCH /api/projects/shop/services/web {"uptime":{"disabled":false}}`},
		{[]string{"uptime", "set-path", "shop", "web", "/health"}, `PATCH /api/projects/shop/services/web {"uptime":{"path":"/health"}}`},
		{[]string{"uptime", "set-path", "shop", "web", ""}, `PATCH /api/projects/shop/services/web {"uptime":{"path":""}}`},
	}
	for _, c := range cases {
		got = ""
		if _, err := runRoot(t, c.args...); err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got != c.want {
			t.Errorf("%v sent %q, want %q", c.args, got, c.want)
		}
	}
}

// OPS-10: a project-level disable wins over a service enable, so the CLI
// must not leave the user believing the service is checked again.
func TestUptimeEnableServiceWarnsWhenProjectDisabled(t *testing.T) {
	for _, c := range []struct {
		project  string
		wantWarn bool
	}{
		{`{"project":{"spec":{"uptime":{"disabled":true}}}}`, true},
		{`{"project":{"spec":{}}}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet && r.URL.Path == "/api/projects/shop" {
				_, _ = io.WriteString(w, c.project)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		}))
		api = &kusoApi.KusoClient{}
		api.Init(srv.URL, "test-token")
		out, err := runRoot(t, "uptime", "enable", "shop", "web")
		srv.Close()
		api = nil
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "warning: uptime checks are disabled for project shop"); got != c.wantWarn {
			t.Errorf("project %s: warned=%v, want %v (output %q)", c.project, got, c.wantWarn, out)
		}
	}
}

func TestUptimeRow(t *testing.T) {
	up := uptimeRow(kusoApi.UptimeServiceStatus{Service: "web", State: "up", LatencyMs: 12, StatusCode: 200})
	if want := []string{"web", "up", "-", "12ms", "-", "-"}; !reflect.DeepEqual(up, want) {
		t.Errorf("up row = %v, want %v", up, want)
	}
	paused := uptimeRow(kusoApi.UptimeServiceStatus{Service: "api", State: "paused", Reason: "asleep"})
	if want := []string{"api", "paused", "-", "-", "-", "asleep"}; !reflect.DeepEqual(paused, want) {
		t.Errorf("paused row = %v, want %v", paused, want)
	}
	down := uptimeRow(kusoApi.UptimeServiceStatus{Service: "api", State: "down", Reason: "x", Error: "connection refused"})
	if down[5] != "connection refused" {
		t.Errorf("detail = %q, want the error to win over the reason", down[5])
	}
}
