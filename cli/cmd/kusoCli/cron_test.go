package kusoCli

import (
	"reflect"
	"testing"
)

func TestSplitCmd(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Plain argv splits on whitespace.
		{"rails runner Cleanup.run", []string{"rails", "runner", "Cleanup.run"}},
		// Quoted segment stays one arg — the case strings.Fields broke.
		{`sh -c "echo tick"`, []string{"sh", "-c", "echo tick"}},
		{`sh -c 'echo tick'`, []string{"sh", "-c", "echo tick"}},
		// Quoted string containing multiple spaces is preserved verbatim.
		{`sh -c "rm -rf /tmp/*"`, []string{"sh", "-c", "rm -rf /tmp/*"}},
		// Empty input yields empty argv, not a one-element slice.
		{"", nil},
		// Unbalanced quote falls back to Fields (best-effort, no error).
		{`sh -c "oops`, []string{"sh", "-c", `"oops`}},
	}
	for _, c := range cases {
		got := splitCmd(c.in)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCmd(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
