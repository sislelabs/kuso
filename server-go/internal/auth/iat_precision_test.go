package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A login right after a role or grant change must not be dead on
// arrival: the revocation watermark is stored to the microsecond, so an
// iat truncated to the second read as issued before it.
func TestTokenIssuedAfterWatermarkIsNotBeforeIt(t *testing.T) {
	iss, err := NewIssuer("0123456789abcdef0123456789abcdef0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	watermark := time.Now()
	tok, err := iss.Sign(Claims{UserID: "u1", Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := iss.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.IssuedAt == nil || c.IssuedAt.Time.Before(watermark) {
		t.Fatalf("iat %v is before a watermark %v set just before signing", c.IssuedAt, watermark)
	}
}

// Tokens minted before the precision change carry integer iat/exp.
func TestWholeSecondTokenStillVerifies(t *testing.T) {
	iss, err := NewIssuer("0123456789abcdef0123456789abcdef0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	prev := jwt.TimePrecision
	jwt.TimePrecision = time.Second
	tok, err := iss.Sign(Claims{UserID: "u1", Username: "u"})
	jwt.TimePrecision = prev
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iss.Verify(tok); err != nil {
		t.Fatalf("whole-second token rejected: %v", err)
	}
}
