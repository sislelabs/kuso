// SSH keypair generation for the server-side key library. Defaults to
// ed25519 — small (32-byte private key), modern, and wireshark-fast
// to parse. Output is OpenSSH-format text so the public key drops
// straight into ~/.ssh/authorized_keys and the private key works with
// `ssh -i`.

package nodejoin

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// Keypair carries both halves in their wire-stable text forms.
type Keypair struct {
	PublicKey   string // "ssh-ed25519 AAAA… kuso@<id>"
	PrivateKey  string // OpenSSH PEM block
	Fingerprint string // "SHA256:abc…" — same format ssh-keygen -lf prints
}

// GenerateEd25519 returns a fresh ed25519 keypair stamped with a
// recognisable comment so the operator can tell which keys came from
// kuso when looking at authorized_keys. Returns the private key in
// the OpenSSH PEM container — the bog-standard format every ssh
// client understands.
func GenerateEd25519(comment string) (*Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("wrap ssh public key: %w", err)
	}
	pubLine := fmt.Sprintf("%s %s %s",
		sshPub.Type(),
		base64.StdEncoding.EncodeToString(sshPub.Marshal()),
		comment,
	)
	privBlock, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(privBlock)
	// Fingerprint = sha256 of the wire-format public key, base64 (no
	// padding) — same shape ssh-keygen -lf emits.
	sum := sha256.Sum256(sshPub.Marshal())
	fp := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	return &Keypair{
		PublicKey:   pubLine,
		PrivateKey:  string(privPEM),
		Fingerprint: fp,
	}, nil
}

// FingerprintOf computes the SSH fingerprint of a public-key line.
// Used when an operator pastes their own existing key — we still want
// to surface a fingerprint in the UI so the key looks the same as a
// generated one.
func FingerprintOf(publicKey string) (string, error) {
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		return "", fmt.Errorf("parse public key: %w", err)
	}
	sum := sha256.Sum256(parsed.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}
