// Package auth handles password verification, JWT issuance/validation,
// and the bearer-token middleware. It MUST stay wire-compatible with the
// TS server's JWT shape so existing CLI tokens keep working through the
// cutover (kuso/acceptance #1).
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// iat/exp carry microseconds, matching the Postgres timestamp of a
// user's revocation watermark. At the default whole-second precision a
// login in the same second as a role, grant or password change got an
// iat before the watermark and was rejected on its first request.
// Integer-second tokens issued earlier still verify.
func init() { jwt.TimePrecision = time.Microsecond }

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
	secret       []byte
	ttl          time.Duration
	revoked      RevocationChecker  // optional; set via SetRevocationChecker
	resolvePerms PermissionResolver // optional; set via SetPermissionResolver
	seen         SeenRecorder       // optional; set via SetSeenRecorder
	tickets      TicketConsumer     // optional; set via SetTicketConsumer
}

// DevJWTSecret is the secret hack/install.sh writes under
// KUSO_INSECURE_SECRETS=1. It is public, so anyone can forge tokens on
// an instance that runs with it.
const DevJWTSecret = "dev-jwt-secret-do-not-use-in-prod-32-chars"

// MinJWTSecretLen is the shortest HS256 secret accepted without the
// insecure-dev opt-in.
const MinJWTSecretLen = 32

// JWTSecretWeakness returns why secret is unsafe to sign with, or ""
// when it is fine. NewIssuer doesn't enforce it: an existing instance
// with a weak secret must keep serving after an upgrade (refusing would
// brick the dashboard), so the caller decides between refusing and
// warning.
func JWTSecretWeakness(secret string) string {
	switch {
	case secret == DevJWTSecret:
		return "JWT_SECRET is the public dev secret from KUSO_INSECURE_SECRETS=1; anyone can forge admin tokens"
	case len(secret) < MinJWTSecretLen:
		return fmt.Sprintf("JWT_SECRET is %d bytes; at least %d random bytes are required", len(secret), MinJWTSecretLen)
	}
	return ""
}

// JWTBootDecision is what the server does with a weak secret at boot.
// A fresh install (no users yet) refuses to start, since nothing is lost
// by fixing the secret first. An existing install keeps running and
// warns: refusing there would take the dashboard down on upgrade, and
// rotating the secret logs everyone out, so it stays a manual step.
// allowInsecure (KUSO_ALLOW_INSECURE_JWT=1) is the explicit dev opt-in.
func JWTBootDecision(secret string, freshInstall, allowInsecure bool) (problem string, refuse bool) {
	problem = JWTSecretWeakness(secret)
	if problem == "" || allowInsecure {
		return problem, false
	}
	return problem, freshInstall
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
	// Always emit a JTI (JWT ID). Revocation lives keyed on this in
	// the RevokedToken table; without one, logout / role-demotion /
	// leaked-CLI-token can't invalidate a specific bearer. 16 random
	// bytes hex-encoded is plenty of entropy and stays small on the
	// wire.
	if c.ID == "" {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return "", fmt.Errorf("auth: jti rand: %w", err)
		}
		c.ID = hex.EncodeToString(buf[:])
	}

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

// wsTicketAudPrefix marks a WebSocket ticket: a short-lived token minted
// over /api (so it carries the CSRF and console-origin checks) that opens
// exactly one /ws/ path. Browsers present it instead of the session
// cookie, which any script on the kuso origin would otherwise ride.
const wsTicketAudPrefix = "kuso-ws:"

// WSTicketTTL bounds how long a minted ticket can open its socket.
const WSTicketTTL = 60 * time.Second

// SignWSTicket mints a ticket for path from the caller's verified claims.
func (i *Issuer) SignWSTicket(c Claims, path string) (string, error) {
	t := Claims{
		UserID: c.UserID, Username: c.Username, Role: c.Role,
		UserGroups: c.UserGroups, Permissions: c.Permissions, Strategy: c.Strategy,
	}
	t.Audience = jwt.ClaimStrings{wsTicketAudPrefix + path}
	return i.signWith(t, time.Now().Add(WSTicketTTL), false)
}

// WSTicketPath returns the /ws/ path a ticket opens, and false for any
// other token.
func (c *Claims) WSTicketPath() (string, bool) {
	for _, a := range c.Audience {
		if p, ok := strings.CutPrefix(a, wsTicketAudPrefix); ok {
			return p, true
		}
	}
	return "", false
}
