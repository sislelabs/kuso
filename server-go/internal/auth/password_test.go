package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyPassword_Bcrypt(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("hunter2", 4) // low cost for fast tests
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := VerifyPassword(hash, "hunter2", ""); err != nil {
		t.Errorf("correct pw rejected: %v", err)
	}
	if err := VerifyPassword(hash, "wrong", ""); err == nil {
		t.Error("wrong pw accepted")
	}
}

func TestVerifyPassword_LegacyHMAC(t *testing.T) {
	t.Parallel()
	const sessionKey = "test-session-key"
	const plain = "v2-era-pw"

	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write([]byte(plain))
	stored := hex.EncodeToString(mac.Sum(nil))

	if err := VerifyPassword(stored, plain, sessionKey); err != nil {
		t.Errorf("legacy correct pw rejected: %v", err)
	}
	if err := VerifyPassword(stored, "wrong", sessionKey); err == nil {
		t.Error("legacy wrong pw accepted")
	}
	// Without a session key the legacy path is disabled.
	if err := VerifyPassword(stored, plain, ""); err == nil {
		t.Error("legacy hash matched without session key — should fail")
	}
}

func TestVerifyPassword_EmptyStored(t *testing.T) {
	t.Parallel()
	if err := VerifyPassword("", "anything", "k"); err == nil {
		t.Error("empty stored hash should never match")
	}
}
