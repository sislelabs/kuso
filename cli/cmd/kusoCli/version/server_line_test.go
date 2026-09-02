package version

import (
	"errors"
	"testing"
)

func TestServerVersionLine(t *testing.T) {
	ServerVersion = nil
	if got := serverVersionLine(); got != "unknown (no instance configured)" {
		t.Errorf("nil hook: %q", got)
	}
	ServerVersion = func() (string, error) { return "", errors.New("not logged in; run 'kuso login'") }
	if got := serverVersionLine(); got != "unknown (not logged in; run 'kuso login')" {
		t.Errorf("error hook: %q", got)
	}
	ServerVersion = func() (string, error) { return "v0.25.27 (https://kuso.example)", nil }
	if got := serverVersionLine(); got != "v0.25.27 (https://kuso.example)" {
		t.Errorf("ok hook: %q", got)
	}
}
