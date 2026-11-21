package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	secretA = "secret-A-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secretB = "secret-B-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// legacyToken mints a token the way Sign did before kids existed: HS256,
// no kid header.
func legacyToken(t *testing.T, secret string) string {
	t.Helper()
	c := Claims{UserID: "u", Username: "u", Role: "admin", Strategy: "token"}
	c.ID = "legacy-jti"
	c.IssuedAt = jwt.NewNumericDate(time.Now())
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign legacy: %v", err)
	}
	return s
}

func headerOf(t *testing.T, tok string) map[string]any {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(tok, &Claims{})
	if err != nil {
		t.Fatalf("parse unverified: %v", err)
	}
	return parsed.Header
}

func rotated(t *testing.T) *Issuer {
	t.Helper()
	iss, err := NewIssuer(secretB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	iss.SetPreviousSecrets([]string{secretA})
	return iss
}

func TestKeyRing_LegacyTokenVerifiesAgainstCurrent(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer(secretA, time.Hour)
	got, err := iss.Verify(legacyToken(t, secretA))
	if err != nil {
		t.Fatalf("kid-less token must verify: %v", err)
	}
	if got.ID != "legacy-jti" {
		t.Errorf("jti = %q", got.ID)
	}
}

func TestKeyRing_SignStampsKidNotSecret(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer(secretA, time.Hour)
	tok, err := iss.Sign(Claims{UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	h := headerOf(t, tok)
	if h["kid"] != KeyID(secretA) {
		t.Fatalf("kid = %v, want %s", h["kid"], KeyID(secretA))
	}
	if h["alg"] != "HS256" {
		t.Fatalf("alg = %v", h["alg"])
	}
	if strings.Contains(tok, secretA) || KeyID(secretA) == KeyID(secretB) {
		t.Fatal("kid must be a digest distinct per secret, never the secret")
	}
}

func TestKeyRing_RotationKeepsOldTokens(t *testing.T) {
	t.Parallel()
	old, _ := NewIssuer(secretA, time.Hour)
	kidTok, err := old.Sign(Claims{UserID: "u"})
	if err != nil {
		t.Fatal(err)
	}
	iss := rotated(t)

	if _, err := iss.Verify(kidTok); err != nil {
		t.Errorf("token with kid(A) must verify after rotation: %v", err)
	}
	if _, err := iss.Verify(legacyToken(t, secretA)); err != nil {
		t.Errorf("kid-less token signed with A must verify after rotation: %v", err)
	}
	newTok, _ := iss.Sign(Claims{UserID: "u"})
	if headerOf(t, newTok)["kid"] != KeyID(secretB) {
		t.Error("new tokens must be signed with the current secret")
	}
	if _, err := iss.Verify(newTok); err != nil {
		t.Errorf("current token: %v", err)
	}
}

func TestKeyRing_UnknownSecretRejected(t *testing.T) {
	t.Parallel()
	iss := rotated(t)
	const secretC = "secret-C-cccccccccccccccccccccccccccccc"
	other, _ := NewIssuer(secretC, time.Hour)
	withKid, _ := other.Sign(Claims{UserID: "u"})

	if _, err := iss.Verify(withKid); err == nil {
		t.Error("token with an unknown kid must be rejected")
	}
	if _, err := iss.Verify(legacyToken(t, secretC)); err == nil {
		t.Error("kid-less token signed with an unknown secret must be rejected")
	}
}

// A known kid pins the key: a token claiming kid(A) but signed with C
// must not fall through to any other secret.
func TestKeyRing_KidPinsSecret(t *testing.T) {
	t.Parallel()
	iss := rotated(t)
	c := Claims{UserID: "u"}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	tok.Header["kid"] = KeyID(secretA)
	s, _ := tok.SignedString([]byte(secretB))
	if _, err := iss.Verify(s); err == nil {
		t.Fatal("kid(A) signed with B must be rejected")
	}

	// An unknown kid doesn't fall back to the kid-less key search, even
	// when the signature would match the current secret.
	bogus := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	bogus.Header["kid"] = "not-a-known-kid"
	bs, _ := bogus.SignedString([]byte(secretB))
	if _, err := iss.Verify(bs); err == nil || !strings.Contains(err.Error(), "unknown kid") {
		t.Fatalf("unknown kid must be rejected, got %v", err)
	}
}

func TestKeyRing_NonStringKidRejected(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer(secretA, time.Hour)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{UserID: "u"})
	tok.Header["kid"] = 7
	s, _ := tok.SignedString([]byte(secretA))
	if _, err := iss.Verify(s); err == nil {
		t.Fatal("numeric kid must be rejected")
	}
}

func TestKeyRing_AlgPinnedToHS256(t *testing.T) {
	t.Parallel()
	iss := rotated(t)
	c := Claims{UserID: "u"}

	none := jwt.NewWithClaims(jwt.SigningMethodNone, c)
	none.Header["kid"] = KeyID(secretB)
	noneStr, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rs := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	rs.Header["kid"] = KeyID(secretB)
	rsStr, _ := rs.SignedString(key)

	hs512 := jwt.NewWithClaims(jwt.SigningMethodHS512, c)
	hs512Str, _ := hs512.SignedString([]byte(secretB))

	for name, s := range map[string]string{"none": noneStr, "RS256": rsStr, "HS512": hs512Str} {
		if _, err := iss.Verify(s); err == nil {
			t.Errorf("alg %s must be rejected", name)
		}
	}
}

func TestSetPreviousSecrets_IgnoresCurrent(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer(secretA, time.Hour)
	iss.SetPreviousSecrets([]string{secretA, "", secretB})
	if len(iss.previous) != 1 || iss.previous[0].kid != KeyID(secretB) {
		t.Fatalf("previous = %+v", iss.previous)
	}
}

func TestParseSecretList(t *testing.T) {
	t.Parallel()
	got := ParseSecretList(" a , ,b,")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %q", got)
	}
	if ParseSecretList("") != nil {
		t.Fatal("empty input must yield nil")
	}
}

func TestPreviousSecretsWeakness(t *testing.T) {
	t.Parallel()
	if p := PreviousSecretsWeakness([]string{secretA}); p != "" {
		t.Errorf("strong: %q", p)
	}
	if p := PreviousSecretsWeakness([]string{secretA, DevJWTSecret}); !strings.Contains(p, "entry 2") {
		t.Errorf("dev secret: %q", p)
	}
}
