// Package auth handles password verification, JWT issuance/validation,
// and the bearer-token middleware. It MUST stay wire-compatible with the
// TS server's JWT shape so existing CLI tokens keep working through the
// cutover (kuso/acceptance #1).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenTimePrecision is how finely iat/exp are written. At the default
// whole second, a login in the same second as a role, grant or password
// change got an iat before the revocation watermark and was rejected on
// its first request. Milliseconds, not microseconds: the claim is a JSON
// float of seconds, and float64 can't hold a microsecond-precision Unix
// time exactly. Integer-second tokens issued earlier still verify.
const TokenTimePrecision = time.Millisecond

func init() { jwt.TimePrecision = TokenTimePrecision }

// IssuedBefore reports whether a token issued at iat predates a user's
// revocation watermark. Parsing the float claim can land one unit of
// TokenTimePrecision low, so a token is revoked only when it is a whole
// unit older than the watermark: one issued within ~1ms of it stays valid.
func IssuedBefore(iat, watermark time.Time) bool {
	if watermark.IsZero() {
		return false
	}
	return iat.Add(TokenTimePrecision).Before(watermark.Truncate(TokenTimePrecision))
}

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
//
// previous holds retired secrets (KUSO_JWT_PREVIOUS_SECRETS) that still
// verify but never sign, so rotating JWT_SECRET doesn't log everyone out
// or kill CI tokens.
type Issuer struct {
	secret       []byte
	kid          string
	previous     []signingKey
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
	return &Issuer{secret: []byte(secret), kid: KeyID(secret), ttl: ttl}, nil
}

type signingKey struct {
	kid    string
	secret []byte
}

// KeyID is the `kid` header stamped on tokens signed with secret: a
// truncated SHA-256 of the secret under a fixed label, so it names the
// key without revealing it.
func KeyID(secret string) string {
	sum := sha256.Sum256([]byte("kuso-jwt-kid\x00" + secret))
	return hex.EncodeToString(sum[:8])
}

// ParseSecretList splits a comma-separated secret list (the
// KUSO_JWT_PREVIOUS_SECRETS format), trimming blanks.
func ParseSecretList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SetPreviousSecrets registers retired secrets that still verify tokens
// (with a matching kid, or with no kid at all) but never sign. Entries
// equal to the current secret are ignored. Call before serving.
func (i *Issuer) SetPreviousSecrets(secrets []string) {
	i.previous = nil
	for _, s := range secrets {
		if s == "" || s == string(i.secret) {
			continue
		}
		i.previous = append(i.previous, signingKey{kid: KeyID(s), secret: []byte(s)})
	}
}

// PreviousSecretsWeakness reports the first unsafe previous secret, or
// "". A weak previous secret is as forgeable as a weak current one,
// because it still verifies tokens.
func PreviousSecretsWeakness(secrets []string) string {
	for n, s := range secrets {
		if p := JWTSecretWeakness(s); p != "" {
			return fmt.Sprintf("KUSO_JWT_PREVIOUS_SECRETS entry %d: %s", n+1, p)
		}
	}
	return ""
}

// verificationKeys picks the secrets a token may be checked against. A
// token with a kid is checked only against the secret with that kid; one
// without (every token minted before kids existed) is tried against the
// current secret, then each previous one.
func (i *Issuer) verificationKeys(t *jwt.Token) (any, error) {
	raw, has := t.Header["kid"]
	if !has {
		keys := []jwt.VerificationKey{i.secret}
		for _, p := range i.previous {
			keys = append(keys, p.secret)
		}
		return jwt.VerificationKeySet{Keys: keys}, nil
	}
	kid, ok := raw.(string)
	if !ok {
		return nil, errors.New("auth: kid header is not a string")
	}
	if kid == i.kid {
		return i.secret, nil
	}
	for _, p := range i.previous {
		if kid == p.kid {
			return p.secret, nil
		}
	}
	return nil, fmt.Errorf("auth: unknown kid %q", kid)
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
	tok.Header["kid"] = i.kid
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
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("auth: unexpected signing method %q", t.Method.Alg())
		}
		return i.verificationKeys(t)
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
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
