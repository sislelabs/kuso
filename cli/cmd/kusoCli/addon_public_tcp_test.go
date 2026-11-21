package kusoCli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/pkg/kusoApi"
)

func TestAddonPublicTCPEnable_RestrictionFlags(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		_, _ = w.Write([]byte(`{"port":30001}`))
	}))
	t.Cleanup(srv.Close)
	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	t.Cleanup(func() { api = nil; resetCmdFlags(addonPublicTCPEnableCmd) })

	base := []string{"project", "addon", "public-tcp", "enable", "shop", "pg"}
	const path = "POST /api/projects/shop/addons/pg/public-tcp "
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{nil, path},
		{[]string{"--allow-cidr", "203.0.113.7/32", "--allow-cidr", "198.51.100.0/24"},
			path + `{"sourceRanges":["203.0.113.7/32","198.51.100.0/24"]}`},
		{[]string{"--clear-allow-cidrs"}, path + `{"sourceRanges":[]}`},
		{[]string{"--max-connections", "0"}, path + `{"maxConnections":0}`},
		{[]string{"--allow-cidr", "10.0.0.0/8", "--max-connections", "5"},
			path + `{"sourceRanges":["10.0.0.0/8"],"maxConnections":5}`},
	} {
		resetCmdFlags(addonPublicTCPEnableCmd)
		bodies = nil
		captureStdout(t, func() {
			if _, err := runRoot(t, append(base, tc.flags...)...); err != nil {
				t.Fatalf("%v: %v", tc.flags, err)
			}
		})
		if len(bodies) != 1 || bodies[0] != tc.want {
			t.Errorf("%v: sent %q, want %q", tc.flags, bodies, tc.want)
		}
	}

	resetCmdFlags(addonPublicTCPEnableCmd)
	bodies = nil
	if _, err := runRoot(t, append(base, "--allow-cidr", "10.0.0.0/8", "--clear-allow-cidrs")...); err == nil {
		t.Error("--allow-cidr with --clear-allow-cidrs: want error")
	}
	if len(bodies) != 0 {
		t.Errorf("conflicting flags still sent %q", bodies)
	}
}
