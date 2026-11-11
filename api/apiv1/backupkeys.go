package v1

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Wire shapes for /api/admin/backup/keys: export and re-import of the
// per-addon backup encryption keys, so encrypted dumps outlive the cluster.

// BackupKeyBundleSchema is the current BackupKeyBundle schema.
const BackupKeyBundleSchema = 1

// BackupKey is one <addon>-backup-key Secret.
type BackupKey struct {
	Namespace string `json:"namespace"`
	Secret    string `json:"secret"`
	Project   string `json:"project,omitempty"`
	Addon     string `json:"addon,omitempty"`
	AddonKind string `json:"addonKind,omitempty"`
	// KeyID is the public fingerprint recorded in each dump's manifest.
	KeyID string `json:"keyId"`
	Key   string `json:"key"`
}

// BackupKeyBundle is what export returns and import accepts.
type BackupKeyBundle struct {
	SchemaVersion int         `json:"schemaVersion"`
	ExportedAt    string      `json:"exportedAt"`
	Keys          []BackupKey `json:"keys"`
}

// BackupKeyExportResponse is the body of GET /api/admin/backup/keys.
// With ?recipient=<public key> the bundle comes back only as Sealed
// (base64 of SealBackupKeys output) and Bundle is nil.
type BackupKeyExportResponse struct {
	Bundle   *BackupKeyBundle `json:"bundle,omitempty"`
	Sealed   string           `json:"sealed,omitempty"`
	Count    int              `json:"count"`
	Warnings []string         `json:"warnings,omitempty"`
}

// BackupKeyImportResult is one key's outcome on import.
type BackupKeyImportResult struct {
	Namespace string `json:"namespace"`
	Secret    string `json:"secret"`
	// Status: created | unchanged | conflict | error.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// BackupKeyImportResponse is the body of POST /api/admin/backup/keys.
type BackupKeyImportResponse struct {
	Results []BackupKeyImportResult `json:"results"`
}

// Sealed bundles: ephemeral X25519 + HKDF-SHA256 + AES-256-GCM to an
// operator-held public key, so an export can sit next to the backups it
// unlocks without being readable by whoever holds the bucket.
const (
	sealedMagic         = "KUSOKEYS1"
	backupKeyPubPrefix  = "kuso-x25519-pub:"
	backupKeyPrivPrefix = "kuso-x25519-priv:"
	sealInfo            = "kuso-backup-keys-v1"
)

// GenerateBackupKeyIdentity returns a new (private, public) key pair in
// their text encodings.
func GenerateBackupKeyIdentity() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return backupKeyPrivPrefix + base64.StdEncoding.EncodeToString(k.Bytes()),
		backupKeyPubPrefix + base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// ParseBackupKeyRecipient decodes a public key from its text encoding.
func ParseBackupKeyRecipient(s string) (*ecdh.PublicKey, error) {
	b, err := decodePrefixed(s, backupKeyPubPrefix)
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPublicKey(b)
}

// ParseBackupKeyIdentity decodes a private key from its text encoding.
func ParseBackupKeyIdentity(s string) (*ecdh.PrivateKey, error) {
	b, err := decodePrefixed(s, backupKeyPrivPrefix)
	if err != nil {
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(b)
}

func decodePrefixed(s, prefix string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, prefix) {
		return nil, fmt.Errorf("expected a %q key", strings.TrimSuffix(prefix, ":"))
	}
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
}

func sealKey(shared, eph, recipient []byte) ([]byte, error) {
	salt := append(append([]byte{}, eph...), recipient...)
	return hkdf.Key(sha256.New, shared, salt, sealInfo, 32)
}

// SealBackupKeys encrypts plaintext to recipient.
func SealBackupKeys(recipient *ecdh.PublicKey, plaintext []byte) ([]byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	key, err := sealKey(shared, ephPub, recipient.Bytes())
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	head := append(append([]byte(sealedMagic), ephPub...), nonce...)
	return gcm.Seal(head, nonce, plaintext, head), nil
}

// OpenBackupKeys decrypts a SealBackupKeys output. Any modification fails.
func OpenBackupKeys(identity *ecdh.PrivateKey, sealed []byte) ([]byte, error) {
	const ephLen, nonceLen = 32, 12
	hl := len(sealedMagic) + ephLen + nonceLen
	if len(sealed) < hl || !bytes.HasPrefix(sealed, []byte(sealedMagic)) {
		return nil, errors.New("not a sealed kuso backup-key bundle")
	}
	head := sealed[:hl]
	ephPub, err := ecdh.X25519().NewPublicKey(head[len(sealedMagic) : len(sealedMagic)+ephLen])
	if err != nil {
		return nil, err
	}
	shared, err := identity.ECDH(ephPub)
	if err != nil {
		return nil, err
	}
	key, err := sealKey(shared, ephPub.Bytes(), identity.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, head[len(head)-nonceLen:], sealed[hl:], head)
	if err != nil {
		return nil, errors.New("bundle does not decrypt with this identity (wrong key or modified file)")
	}
	return pt, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
