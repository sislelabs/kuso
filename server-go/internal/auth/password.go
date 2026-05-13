package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// VerifyPassword checks a plaintext password against a stored bcrypt
// hash. Returns nil on success, errInvalidCredentials otherwise.
func VerifyPassword(stored, plaintext string) error {
	if stored == "" {
		return errors.New("auth: stored password empty")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(plaintext)); err != nil {
		return errInvalidCredentials
	}
	return nil
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
