package tools

import "testing"

func TestAPIPathEscapes(t *testing.T) {
	cases := []struct {
		segments []string
		want     string
	}{
		{[]string{"api", "projects", "web"}, "/api/projects/web"},
		{[]string{"api", "projects", "a/b", "services", "web"}, "/api/projects/a%2Fb/services/web"},
		{[]string{"api", "projects", "p", "addons", "pg?confirm=pg"}, "/api/projects/p/addons/pg%3Fconfirm=pg"},
		{[]string{"api", "projects", "a b"}, "/api/projects/a%20b"},
	}
	for _, c := range cases {
		if got := apiPath(c.segments...); got != c.want {
			t.Errorf("apiPath(%v) = %q, want %q", c.segments, got, c.want)
		}
	}
}
