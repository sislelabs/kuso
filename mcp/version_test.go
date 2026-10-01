package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	vcs := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "a00e5332abcdef0123456789"},
		{Key: "vcs.modified", Value: "true"},
	}}
	cases := []struct {
		name    string
		stamped string
		bi      *debug.BuildInfo
		want    string
	}{
		{"ldflags wins", "v0.27.0", vcs, "v0.27.0"},
		{"module version", "", &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, "v1.2.3"},
		{"vcs revision", "", vcs, "dev-a00e5332abcd-dirty"},
		{"nothing known", "", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"no build info", "", nil, "dev"},
	}
	for _, c := range cases {
		if got := resolveVersion(c.stamped, c.bi); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
