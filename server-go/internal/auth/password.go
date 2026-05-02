package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// VerifyPassword checks a plaintext password against the stored hash.
//
// Two formats are accepted, matching the TS auth.service.ts logic:
//  1. bcrypt — the default for any user created in v3+.
//  2. legacy HMAC-SHA256 hex digest with KUSO_SESSION_KEY as the key.
//     This is for users still carrying a v2-era hash; the TS code
//     compares both and treats either match as success.
//
// sessionKey may be empty if the user has no legacy hash to fall back on
// (the bcrypt path doesn't need it). Returns nil on success, a non-nil
// error otherwise — never indicates *which* path failed, to prevent
// timing oracles.
func VerifyPassword(stored, plaintext, sessionKey string) error {
	if stored == "" {
		return errors.New("auth: stored password empty")
	}
	// bcrypt hashes always start with $2 — fast pre-check avoids paying
	// the bcrypt cost on legacy rows.
	if len(stored) > 4 && stored[0] == '$' && stored[1] == '2' {
		if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(plaintext)); err == nil {
			return nil
		}
		return errInvalidCredentials
	}
	// Legacy HMAC-SHA256 path: only attempt if a session key is wired.
	if sessionKey == "" {
		return errInvalidCredentials
	}
	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write([]byte(plaintext))
	digest := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(stored), []byte(digest)) == 1 {
		return nil
	}
	return errInvalidCredentials
}

// errInvalidCredentials is the single sentinel both paths return so a
// caller can't distinguish bcrypt-fail from legacy-fail.
var errInvalidCredentials = errors.New("auth: invalid credentials")

// HashPassword produces a bcrypt hash suitable for storing in User.password.
// Callers use this for password change + new-user flows. cost defaults to
// bcrypt.DefaultCost (10) when 0 is passed.
func HashPassword(plaintext string, cost int) (string, error) {
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}
