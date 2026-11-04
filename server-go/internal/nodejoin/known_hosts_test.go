package nodejoin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/ssh"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func newHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// LOOP-17: pins lived in the container's ephemeral home dir, so every
// restart reset trust-on-first-use. With the Secret store a pin made by
// one process must still reject a different key after a "restart".
func TestHostKeyPinSurvivesRestart(t *testing.T) {
	t.Setenv("KUSO_NODEJOIN_INSECURE_HOSTKEY", "")
	t.Setenv("KUSO_NODEJOIN_KNOWN_HOSTS", t.TempDir()+"/kh") // must not be used
	cs := kubefake.NewSimpleClientset()
	SetKnownHostStore(&SecretKnownHosts{Client: cs, Namespace: "kuso"})
	t.Cleanup(func() { SetKnownHostStore(nil) })

	good, evil := newHostKey(t), newHostKey(t)
	cb, err := buildHostKeyCallback()
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("node-2:22", nil, good); err != nil {
		t.Fatalf("first contact should pin: %v", err)
	}

	// Fresh callback on a fresh container filesystem; the pin must come
	// from the Secret.
	t.Setenv("KUSO_NODEJOIN_KNOWN_HOSTS", t.TempDir()+"/kh")
	cb2, _ := buildHostKeyCallback()
	if err := cb2("node-2:22", nil, evil); err == nil {
		t.Error("different key accepted after restart")
	}
	if err := cb2("node-2:22", nil, good); err != nil {
		t.Errorf("pinned key rejected: %v", err)
	}
	if got, _ := (&SecretKnownHosts{Client: cs, Namespace: "kuso"}).Lookup(context.Background(), "[fd00::1]:22"); got != nil {
		t.Error("unrelated host must have no pin")
	}
}

// The dial must use the address that passed the loopback/link-local
// check rather than resolving the name a second time.
func TestRejectLoopbackReturnsVettedIP(t *testing.T) {
	ip, err := rejectLoopbackOrLinkLocal(context.Background(), "10.0.0.7")
	if err != nil || ip.String() != "10.0.0.7" {
		t.Fatalf("got %v, %v; want 10.0.0.7", ip, err)
	}
	if _, err := rejectLoopbackOrLinkLocal(context.Background(), "169.254.169.254"); err == nil {
		t.Error("metadata address must be refused")
	}
}
