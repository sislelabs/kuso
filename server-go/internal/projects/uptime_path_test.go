package projects

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeUptimePath(t *testing.T) {
	for in, want := range map[string]string{"": "", "  ": "", "/": "/", " /health ": "/health", "/a?deep=1": "/a?deep=1"} {
		got, err := normalizeUptimePath(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"health", "//evil.com/x", "http://evil.com/", "/a b", "/a\nb", "/a\\b", "/" + strings.Repeat("a", 512)} {
		if _, err := normalizeUptimePath(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted (err=%v)", bad, err)
		}
	}
}
