package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// VerifySignature checks the X-Hub-Signature-256 header against the raw
// request body using the configured webhook secret.
//
// Constant-time compare via crypto/hmac.Equal so timing attacks can't
// reveal the secret. Returns false on any malformed input rather than
// erroring — a 4xx body is fine, callers just need a yes/no.
func VerifySignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	want, err := hex.DecodeString(signature[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(mac.Sum(nil), want)
}

// ErrUnverifiedSignature is returned by Receiver when the signature
// header doesn't match. Routed to 400 in the controller.
var ErrUnverifiedSignature = errors.New("github: invalid webhook signature")
