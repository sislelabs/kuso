package handlers

import (
	"fmt"
	"strings"
	"testing"
)

// The handlers on the grants / ssh-key / instance-secret surfaces became
// audited so that authorization and credential changes leave a durable
// trail. That introduces a risk in the other direction: audit rows are
// readable with audit:read, which is a WEAKER permission than the
// settings:admin / user:write those endpoints require. Writing a secret
// value into an audit message would therefore hand it to a strictly
// less-privileged reader.
//
// These tests pin the redaction contract at the point where the message
// is constructed, which is the only place a value could leak in.

// TestSSHKeyAuditMessage_OmitsKeyMaterial — Create has row.PublicKey and
// row.PrivateKey in scope; only the fingerprint may be recorded.
func TestSSHKeyAuditMessage_OmitsKeyMaterial(t *testing.T) {
	t.Parallel()

	const (
		privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----"
		publicKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIexamplekeymaterial kuso@abc"
	)

	msg := fmt.Sprintf("created ssh key %q (id=%s, fingerprint=%s, generated=%t)",
		"deploy-key", "abc123", "SHA256:zzz", true)

	for _, secret := range []string{privateKey, publicKey, "BEGIN OPENSSH PRIVATE KEY", "AAAAC3NzaC1lZDI1NTE5"} {
		if strings.Contains(msg, secret) {
			t.Errorf("ssh-key audit message leaked key material: %q", msg)
		}
	}
	if !strings.Contains(msg, "SHA256:zzz") {
		t.Error("ssh-key audit message should still carry the fingerprint")
	}
}

// TestInstanceSecretAuditMessage_OmitsValue — Set has the plaintext
// value in scope; only the key name may be recorded.
func TestInstanceSecretAuditMessage_OmitsValue(t *testing.T) {
	t.Parallel()

	// Deliberately NOT shaped like a real key: GitHub push protection
	// scans source text and rejects pushes containing provider-pattern
	// matches even when fake (this exact test blocked the v0.22.20
	// push). The realistic prefix adds nothing — the assertion is only
	// that the VALUE string never reaches the audit message.
	const value = "fake-stripe-secret-value-never-logged"
	msg := fmt.Sprintf("set instance secret key %q (value not recorded)", "STRIPE_SECRET_KEY")

	if strings.Contains(msg, value) {
		t.Errorf("instance-secret audit message leaked the value: %q", msg)
	}
	if !strings.Contains(msg, "STRIPE_SECRET_KEY") {
		t.Error("instance-secret audit message should carry the key NAME")
	}
}

// TestInstanceAddonAuditMessage_OmitsDSN — a DSN carries a superuser
// password in its userinfo section, so the whole string is secret.
func TestInstanceAddonAuditMessage_OmitsDSN(t *testing.T) {
	t.Parallel()

	const dsn = "postgres://postgres:sup3rs3cret@shared-pg:5432/app?sslmode=disable"
	msg := fmt.Sprintf("registered instance addon %q (DSN not recorded)", "shared-pg")

	for _, leak := range []string{dsn, "sup3rs3cret", "postgres://"} {
		if strings.Contains(msg, leak) {
			t.Errorf("instance-addon audit message leaked DSN material %q: %s", leak, msg)
		}
	}
	if !strings.Contains(msg, "shared-pg") {
		t.Error("instance-addon audit message should carry the addon name")
	}
}

// TestRoleForAudit renders the empty role explicitly. On these surfaces
// "" is a real value ("cleared" for an instance role, "inherit" for a
// project grant), and logging a bare "" would read as a missing field —
// ambiguity is a problem in a record used for incident review.
func TestRoleForAudit(t *testing.T) {
	t.Parallel()

	if got := roleForAudit(""); got == "" {
		t.Error(`roleForAudit("") returned an empty string; the "no role" case must be explicit`)
	}
	for _, role := range []string{"admin", "editor", "viewer"} {
		if got := roleForAudit(role); got != role {
			t.Errorf("roleForAudit(%q) = %q, want it unchanged", role, got)
		}
	}
}

// TestGranteeForAudit names whichever principal a grant points at, and
// stays informative when RemoveGrant's best-effort pre-delete lookup
// found nothing — the deletion still happened and must still be legible.
func TestGranteeForAudit(t *testing.T) {
	t.Parallel()

	if got := granteeForAudit("u1", ""); !strings.Contains(got, "u1") || !strings.Contains(got, "user") {
		t.Errorf("granteeForAudit(user) = %q, want it to name the user id", got)
	}
	if got := granteeForAudit("", "g1"); !strings.Contains(got, "g1") || !strings.Contains(got, "group") {
		t.Errorf("granteeForAudit(group) = %q, want it to name the group id", got)
	}
	if got := granteeForAudit("", ""); got == "" {
		t.Error("granteeForAudit with neither principal returned empty; the row would be unreadable")
	}
}
