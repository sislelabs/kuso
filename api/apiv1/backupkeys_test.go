package v1

import (
	"bytes"
	"testing"
)

func TestSealBackupKeys_RoundTripAndTamper(t *testing.T) {
	priv, pub, err := GenerateBackupKeyIdentity()
	if err != nil {
		t.Fatal(err)
	}
	rcpt, err := ParseBackupKeyRecipient(pub)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ParseBackupKeyIdentity(priv)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(`{"keys":[{"key":"s3cret"}]}`)
	sealed, err := SealBackupKeys(rcpt, msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("s3cret")) {
		t.Fatal("sealed bundle carries the plaintext")
	}
	got, err := OpenBackupKeys(id, sealed)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	for _, i := range []int{len(sealedMagic) + 1, len(sealedMagic) + 40, len(sealed) - 1} {
		bad := append([]byte{}, sealed...)
		bad[i] ^= 1
		if _, err := OpenBackupKeys(id, bad); err == nil {
			t.Errorf("tampered byte %d accepted", i)
		}
	}
	otherPriv, _, _ := GenerateBackupKeyIdentity()
	other, _ := ParseBackupKeyIdentity(otherPriv)
	if _, err := OpenBackupKeys(other, sealed); err == nil {
		t.Error("opened with the wrong identity")
	}
	if _, err := ParseBackupKeyRecipient(priv); err == nil {
		t.Error("a private key parsed as a recipient")
	}
}
