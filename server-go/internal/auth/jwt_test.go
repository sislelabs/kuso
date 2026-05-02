package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestIssuer_RoundTrip(t *testing.T) {
	t.Parallel()
	iss, err := NewIssuer("super-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}

	want := Claims{
		UserID:      "user-1",
		Username:    "admin",
		Role:        "admin",
		UserGroups:  []string{"ops"},
		Permissions: []string{"app:read", "app:write"},
		Strategy:    "local",
	}
	tok, err := iss.Sign(want)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := iss.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.UserID != want.UserID || got.Username != want.Username || got.Strategy != want.Strategy {
		t.Errorf("identity: %+v", got)
	}
	if got.Subject != want.UserID {
		t.Errorf("Subject mirror: got %q, want %q", got.Subject, want.UserID)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "app:read" {
		t.Errorf("permissions: %+v", got.Permissions)
	}
	if got.ExpiresAt == nil || got.ExpiresAt.Time.Before(time.Now()) {
		t.Errorf("ExpiresAt missing or in the past: %+v", got.ExpiresAt)
	}
}

func TestIssuer_RejectsWrongSecret(t *testing.T) {
	t.Parallel()
	a, _ := NewIssuer("secret-A", time.Hour)
	b, _ := NewIssuer("secret-B", time.Hour)

	tok, err := a.Sign(Claims{UserID: "x", Username: "x", Role: "x"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if _, err := b.Verify(tok); err == nil {
		t.Fatal("expected verify failure for cross-secret token")
	}
}

func TestIssuer_RejectsExpired(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Millisecond)
	tok, _ := iss.Sign(Claims{UserID: "x", Username: "x", Role: "x"})
	time.Sleep(20 * time.Millisecond)
	if _, err := iss.Verify(tok); err == nil {
		t.Fatal("expected expired token to fail verification")
	}
}

func TestIssuer_RejectsAlgNone(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)

	// Forge an unsigned token. ParseWithClaims must reject it because
	// our keyfunc demands HMAC.
	c := Claims{UserID: "x", Username: "x", Role: "x"}
	t1 := jwt.NewWithClaims(jwt.SigningMethodNone, c)
	str, err := t1.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	if _, err := iss.Verify(str); err == nil || !strings.Contains(err.Error(), "signing method") {
		t.Fatalf("expected alg-rejection, got %v", err)
	}
}

func TestNewIssuer_RejectsEmptySecret(t *testing.T) {
	t.Parallel()
	if _, err := NewIssuer("", time.Hour); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
