package auth

import (
	"testing"
)

func TestVerifyPassword(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("hunter2", 4) // low cost for fast tests
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := VerifyPassword(hash, "hunter2"); err != nil {
		t.Errorf("correct pw rejected: %v", err)
	}
	if err := VerifyPassword(hash, "wrong"); err == nil {
		t.Error("wrong pw accepted")
	}
	if err := VerifyPassword("", "anything"); err == nil {
		t.Error("empty stored hash should never match")
	}
}
