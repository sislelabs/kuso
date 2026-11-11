package kusoCli

import "testing"

func TestLastBuildLinePicksNewest(t *testing.T) {
	builds := []statusBuild{
		{ID: "b-new", Status: "failed", Branch: "main", StartedAt: "2026-09-28T10:00:00Z", ErrorMessage: "npm ERR! missing script: build"},
		{ID: "b-old", Status: "succeeded", StartedAt: "2026-09-20T10:00:00Z"},
	}
	// Archived records come after live CRs, so put the newest last too.
	builds = append(builds[1:], builds[0])
	want := "last build: failed  b-new  branch=main\n    npm ERR! missing script: build"
	if got := lastBuildLine(builds); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := lastBuildLine(nil); got != "last build: none" {
		t.Errorf("empty: got %q", got)
	}
}

func TestIsolationWarning(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		np   *statusProjectNetworkPolicy
		want string
	}{
		{nil, "env isolation off"},
		{&statusProjectNetworkPolicy{EnvIsolation: &on}, "non-production envs"},
		{&statusProjectNetworkPolicy{EnvIsolation: &on, IsolateNonProdEnvs: &on}, ""},
		{&statusProjectNetworkPolicy{Enabled: &off}, ""},
	} {
		got := isolationWarning("shop", tc.np)
		if (tc.want == "") != (got == "") || len(got) < len(tc.want) || got[:len(tc.want)] != tc.want {
			t.Errorf("%+v: got %q, want prefix %q", tc.np, got, tc.want)
		}
	}
}

func TestAddonsStatusLines(t *testing.T) {
	addons := []map[string]any{
		{"metadata": map[string]any{"name": "shop-db"}, "spec": map[string]any{"kind": "postgres", "version": "16"}},
		{"metadata": map[string]any{"name": "shop-cache"}, "spec": map[string]any{"kind": "redis"}},
	}
	if got := addonsStatusLines("shop", addons); got != "\naddons: cache (redis), db (postgres 16)\n" {
		t.Errorf("got %q", got)
	}
}
