// Package auth handles password verification, JWT issuance/validation,
// and the bearer-token middleware. It MUST stay wire-compatible with the
// TS server's JWT shape so existing CLI tokens keep working through the
// cutover (kuso/docs/REWRITE.md §8 acceptance #1).
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims mirrors the payload the TS auth.service.ts emits via JwtService.
//
// Field tags match the TS shape exactly — the existing CLI parses these
// keys, and the JWT spec is case-sensitive on field names. `sub` is added
// in addition to `userId` as a small forward-compat hedge; the TS jwt
// strategy ignores fields it does not look up by name.
type Claims struct {
	UserID      string   `json:"userId"`
	Username    string   `json:"username"`
	Role        string   `json:"role"`
	UserGroups  []string `json:"userGroups"`
	Permissions []string `json:"permissions"`
	Strategy    string   `json:"strategy"`

	jwt.RegisteredClaims
}

// Issuer signs and verifies tokens with HS256. The secret is shared with
// the TS server (process.env.JWT_SECRET) so tokens round-trip between
// implementations during the staged cutover.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// NewIssuer constructs an Issuer with the given HMAC secret and TTL.
//
// Empty secret is a deliberate misconfiguration — refuse rather than fall
// back to a default like the TS code does, because a default secret in
// the binary would let any client forge tokens.
func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if secret == "" {
		return nil, errors.New("auth: empty JWT secret; set JWT_SECRET in the environment")
	}
	if ttl <= 0 {
		ttl = 10 * time.Hour // matches TS default of 36000s
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

// Sign issues a JWT with the given claim payload. exp is set from ttl.
//
// strategy mirrors the TS field — "local" for password login, "oauth2"
// for OAuth callback, "token" for long-lived API tokens issued through
// the /api/tokens endpoint.
func (i *Issuer) Sign(c Claims) (string, error) {
	return i.signWith(c, time.Now().Add(i.ttl), false)
}

// SignWithExpiry issues a JWT with a caller-provided expiry. Pass
// the zero time to mint a non-expiring token (what the personal
// access token "never" option uses). Verify intentionally allows
// claims without exp; we use it deliberately for long-lived API
// tokens.
func (i *Issuer) SignWithExpiry(c Claims, expiresAt time.Time) (string, error) {
	return i.signWith(c, expiresAt, expiresAt.IsZero())
}

func (i *Issuer) signWith(c Claims, expiresAt time.Time, omitExp bool) (string, error) {
	now := time.Now()
	c.IssuedAt = jwt.NewNumericDate(now)
	if omitExp {
		c.ExpiresAt = nil
	} else {
		c.ExpiresAt = jwt.NewNumericDate(expiresAt)
	}
	c.Subject = c.UserID

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign: %w", err)
	}
	return signed, nil
}

// Verify parses + validates a token string. Returns the claims if and
// only if the signature is valid, the algorithm is HS256, and the
// expiration has not passed.
func (i *Issuer) Verify(tokenStr string) (*Claims, error) {
	var c Claims
	tok, err := jwt.ParseWithClaims(tokenStr, &c, func(t *jwt.Token) (any, error) {
		// Reject any algorithm other than HMAC-SHA256. This guards
		// against "alg=none" downgrades and RS256-key-confusion attacks.
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %q", t.Method.Alg())
		}
		return i.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("auth: verify: %w", err)
	}
	if !tok.Valid {
		return nil, errors.New("auth: token invalid")
	}
	return &c, nil
}
