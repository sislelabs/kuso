package projects

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRuntime(t *testing.T) {
	cases := []struct {
		in        string
		wantOK    bool
		wantSubstr string
	}{
		{"", true, ""},
		{"dockerfile", true, ""},
		{"nixpacks", false, "not supported yet"},
		{"buildpacks", false, "not supported yet"},
		{"static", false, "not supported yet"},
		{"wat", false, "unknown runtime"},
	}
	for _, c := range cases {
		err := validateRuntime(c.in)
		if c.wantOK {
			if err != nil {
				t.Errorf("validateRuntime(%q) want ok, got %v", c.in, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("validateRuntime(%q) want err, got nil", c.in)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("validateRuntime(%q) want errors.Is(err, ErrInvalid); err=%v", c.in, err)
		}
		if c.wantSubstr != "" && !strings.Contains(err.Error(), c.wantSubstr) {
			t.Errorf("validateRuntime(%q) error %q missing %q", c.in, err.Error(), c.wantSubstr)
		}
	}
}
