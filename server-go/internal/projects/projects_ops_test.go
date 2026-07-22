package projects

import (
	"errors"
	"testing"
)

func TestValidateProjectName(t *testing.T) {
	good := []string{"acme", "my-app", "app123", "a"}
	for _, n := range good {
		if err := validateProjectName(n); err != nil {
			t.Errorf("validateProjectName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"Acme", "my_app", "-lead", "trail-", "has space", "way-too-long-project-name-that-exceeds-the-forty-char-budget"}
	for _, n := range bad {
		err := validateProjectName(n)
		if err == nil {
			t.Errorf("validateProjectName(%q) = nil, want ErrInvalid", n)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("validateProjectName(%q) err not ErrInvalid: %v", n, err)
		}
	}
}
