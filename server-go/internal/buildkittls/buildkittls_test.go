package buildkittls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
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

// OPS-4: the one-step enable refuses a daemon that can't do TLS, and on a
// current one flips the switch and restarts the daemon.
func TestSetEnabled(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	if err := s.SetEnabled(ctx, true); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("no daemon: err = %v", err)
	}
	pods := []*corev1.Pod{daemonPod("kuso-buildkitd-a"), daemonPod("kuso-buildkitd-b")}
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods("kuso").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetEnabled(ctx, true); !errors.Is(err, ErrDaemonPredatesTLS) {
		t.Fatalf("old daemon: err = %v", err)
	}
	if on, _ := s.Enabled(ctx); on {
		t.Fatal("enabled on a daemon that can't serve TLS")
	}

	mountTLS(t, cs, pods)
	if err := s.SetEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.Enabled(ctx); !on {
		t.Fatal("not enabled")
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(ctx, SecretName, metav1.GetOptions{})
	if !complete(sec.Data) {
		t.Fatal("enabled without a full certificate set")
	}
	if left, _ := cs.CoreV1().Pods("kuso").List(ctx, metav1.ListOptions{}); len(left.Items) != 0 {
		t.Fatalf("daemons not restarted: %d pods left", len(left.Items))
	}
}

// ISO-1: an upgraded install (secret minted enabled=false) gets client auth
// turned on automatically, but only on a daemon that mounts the Secret,
// only while no build is in flight, never over an operator's explicit
// choice, and with exactly one daemon restart.
func TestAutoEnable(t *testing.T) {
	ctx := context.Background()
	s, cs := newStore()
	pods := []*corev1.Pod{daemonPod("kuso-buildkitd-a"), daemonPod("kuso-buildkitd-b")}
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods("kuso").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	idle := func(context.Context) (bool, error) { return false, nil }
	busy := func(context.Context) (bool, error) { return true, nil }
	// A restart deletes every daemon pod: count restarts as delete rounds.
	restarts := func() int {
		n := 0
		for _, a := range cs.Actions() {
			if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
				n++
			}
		}
		return n / len(pods)
	}

	if done, _, err := s.AutoEnable(ctx, idle); done || !errors.Is(err, ErrDaemonPredatesTLS) {
		t.Fatalf("old daemon: done=%v err=%v", done, err)
	}
	if on, _ := s.Enabled(ctx); on {
		t.Fatal("enabled on a daemon that can't serve TLS")
	}

	mountTLS(t, cs, pods)
	if done, flipped, err := s.AutoEnable(ctx, busy); done || flipped || err != nil {
		t.Fatalf("builds in flight: done=%v flipped=%v err=%v", done, flipped, err)
	}
	if on, _ := s.Enabled(ctx); on || restarts() != 0 {
		t.Fatal("enabled or restarted while builds were in flight")
	}

	if done, flipped, err := s.AutoEnable(ctx, idle); !done || !flipped || err != nil {
		t.Fatalf("idle: done=%v flipped=%v err=%v", done, flipped, err)
	}
	if on, _ := s.Enabled(ctx); !on {
		t.Fatal("not enabled once idle")
	}
	if n := restarts(); n != 1 {
		t.Fatalf("daemon restarts = %d, want 1", n)
	}
	for _, p := range pods { // the DaemonSet recreates them
		p.ResourceVersion = ""
		if _, err := cs.CoreV1().Pods("kuso").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if done, flipped, err := s.AutoEnable(ctx, idle); !done || flipped || err != nil || restarts() != 1 {
		t.Fatalf("second call: done=%v flipped=%v err=%v restarts=%d", done, flipped, err, restarts())
	}

	// An operator's explicit "off" sticks.
	if err := s.SetEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	called := false
	if done, flipped, err := s.AutoEnable(ctx, func(context.Context) (bool, error) { called = true; return false, nil }); !done || flipped || err != nil {
		t.Fatalf("operator off: done=%v flipped=%v err=%v", done, flipped, err)
	}
	if on, _ := s.Enabled(ctx); on || called {
		t.Fatalf("operator's off overridden: on=%v busy consulted=%v", on, called)
	}
}

func daemonPod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
		Labels: map[string]string{"app.kubernetes.io/name": "kuso-buildkitd"}}}
}

// mountTLS gives every daemon pod the tls volume, as a current
// deploy/buildkitd.yaml does. Until the LAST one has it, enabling is refused.
func mountTLS(t *testing.T, cs *fake.Clientset, pods []*corev1.Pod) {
	t.Helper()
	ctx := context.Background()
	for i, p := range pods {
		p.Spec.Volumes = []corev1.Volume{{Name: "tls", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: SecretName},
		}}}
		if _, err := cs.CoreV1().Pods("kuso").Update(ctx, p, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if i < len(pods)-1 {
			if err := daemonsReady(ctx, cs); !errors.Is(err, ErrDaemonPredatesTLS) {
				t.Fatalf("one daemon still without the tls volume: err = %v", err)
			}
		}
	}
}

func daemonsReady(ctx context.Context, cs *fake.Clientset) error {
	return (&Store{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}).daemonMountsSecret(ctx)
}
