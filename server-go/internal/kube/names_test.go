package kube

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateReleaseName(t *testing.T) {
	if err := ValidateReleaseName(strings.Repeat("a", 53)); err != nil {
		t.Fatalf("53 chars should pass: %v", err)
	}
	if err := ValidateReleaseName(strings.Repeat("a", 54)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("54 chars: want ErrNameTooLong, got %v", err)
	}
	if err := ValidateLabelValue(strings.Repeat("a", 63)); err != nil {
		t.Fatalf("63 chars should pass: %v", err)
	}
	if err := ValidateLabelValue(strings.Repeat("a", 64)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("64 chars: want ErrNameTooLong, got %v", err)
	}
}
