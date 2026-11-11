package buildkittls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func newStore() (*Store, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	return &Store{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}, cs
}

func TestMint_ChainsVerify(t *testing.T) {
	set, err := Mint(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(set[KeyCA]) {
		t.Fatal("bad CA PEM")
	}
	srv, err := tls.X509KeyPair(set[KeyServerCert], set[KeyServerKey])
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(srv.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: ServerName, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("server cert: %v", err)
	}
	cli, err := tls.X509KeyPair(set[KeyClientCert], set[KeyClientKey])
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ = x509.ParseCertificate(cli.Certificate[0])
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("client cert: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err == nil {
		t.Fatal("client cert must not be usable as a server cert")
	}
	// KUSO_BUILDKIT_TLS_DIR=<dir> writes a set out for a manual check
	// against a real moby/buildkit daemon.
	if dir := os.Getenv("KUSO_BUILDKIT_TLS_DIR"); dir != "" {
		set[keyEnabled] = []byte("true")
		for k, v := range set {
			if err := os.WriteFile(filepath.Join(dir, k), v, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Installs that predate mTLS get the material minted but stay disabled; a
// fresh install's enabled=true placeholder is completed with the switch
// kept on.
func TestEnsure_DisabledByDefaultAndCompletesPlaceholder(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	if err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if string(sec.Data["enabled"]) != "false" || !complete(sec.Data) {
		t.Fatalf("minted secret: enabled=%q complete=%v", sec.Data["enabled"], complete(sec.Data))
	}
	ca := string(sec.Data[KeyCA])
	if err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ = cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if string(sec.Data[KeyCA]) != ca {
		t.Fatal("second Ensure re-minted a complete secret")
	}

	s2, cs2 := newStore()
	_, _ = cs2.CoreV1().Secrets("kuso").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: "kuso"},
		Data:       map[string][]byte{"enabled": []byte("true")},
	}, metav1.CreateOptions{})
	if err := s2.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	sec, _ = cs2.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if string(sec.Data["enabled"]) != "true" || !complete(sec.Data) {
		t.Fatalf("placeholder: enabled=%q complete=%v", sec.Data["enabled"], complete(sec.Data))
	}
}

// Build namespaces get the CA and the CLIENT pair only: the daemon's
// serving key never leaves the home namespace.
func TestEnsureClientSecret_MirrorsClientPairOnly(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	if name, err := s.EnsureClientSecret(ctx, "proj"); err != nil || name != "" {
		t.Fatalf("before mint: name=%q err=%v", name, err)
	}
	if err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	name, err := s.EnsureClientSecret(ctx, "proj")
	if err != nil || name != ClientSecretName {
		t.Fatalf("name=%q err=%v", name, err)
	}
	home, _ := cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	cli, _ := cs.CoreV1().Secrets("proj").Get(ctx, ClientSecretName, metav1.GetOptions{})
	if string(cli.Data["tls.crt"]) != string(home.Data[KeyClientCert]) || string(cli.Data["tls.key"]) != string(home.Data[KeyClientKey]) {
		t.Fatal("client secret does not carry the client pair")
	}
	for k, v := range cli.Data {
		if string(v) == string(home.Data[KeyServerKey]) {
			t.Fatalf("server key leaked into the build namespace under %q", k)
		}
	}
}
