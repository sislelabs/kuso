package kusoCli

import "testing"

func TestSplitKV(t *testing.T) {
	cases := []struct {
		in     string
		wantK  string
		wantV  string
		wantOK bool
	}{
		{"role=web", "role", "web", true},
		{"gpu=", "gpu", "", true},         // explicit empty value
		{"gpu", "gpu", "", true},          // bare key = valueless flag
		{"  gpu  ", "gpu", "", true},      // trimmed
		{"foo = bar", "foo", "bar", true}, // trimmed around =
		{"=web", "", "", false},           // leading = (empty key) rejected
		{"", "", "", false},               // empty rejected
		{"  ", "", "", false},             // whitespace-only rejected
	}
	for _, c := range cases {
		k, v, ok := splitKV(c.in)
		if k != c.wantK || v != c.wantV || ok != c.wantOK {
			t.Errorf("splitKV(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, k, v, ok, c.wantK, c.wantV, c.wantOK)
		}
	}
}
