package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestUptimeCommandsSendExpectedPatch(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = r.Method + " " + r.URL.Path + " " + string(body)
		w.Header().Set("Content-Type", "application/json")
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
