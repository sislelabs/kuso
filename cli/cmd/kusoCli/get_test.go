package kusoCli

import (
	"encoding/json"
	"testing"
)

func TestAsString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"hello", "hello"},
		{42, "42"},
		{true, "true"},
	}
	for _, tc := range cases {
		if got := asString(tc.in); got != tc.want {
			t.Errorf("asString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Env-group clones are spec.kind=production; the GROUP column must come
// from the env label so staging doesn't read as production.
func TestEnvGroupOf(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"metadata":{"labels":{"kuso.sislelabs.com/env":"staging"}},"spec":{"kind":"production"}}`, "staging"},
		{`{"metadata":{"labels":{"kuso.sislelabs.com/env":"preview-pr-7"}},"spec":{"kind":"preview"}}`, "preview-pr-7"},
		{`{"metadata":{"name":"legacy"},"spec":{"kind":"production"}}`, "production"},
	}
	for _, tc := range cases {
		var e map[string]any
		if err := json.Unmarshal([]byte(tc.raw), &e); err != nil {
			t.Fatal(err)
		}
		if got := envGroupOf(e); got != tc.want {
			t.Errorf("envGroupOf(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
