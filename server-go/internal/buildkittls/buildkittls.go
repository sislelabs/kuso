// Package buildkittls mints and distributes the mutual-TLS material for the
// shared kuso-buildkitd daemon (review OPS-3).
//
// The daemon is privileged and its gRPC port lets a client run any build,
// prune every cache and read other projects' cache mounts. Build pods also
// run user code (static buildCmd, buildpacks scripts), and Dockerfile RUN
// steps share the daemon's own network namespace, so a NetworkPolicy alone
// cannot keep user code off the port. With mTLS on, only holders of the
// client certificate get in, and that certificate is mounted into the
// buildctl container of a build Job and nowhere else.
//
// State lives in one Secret, kuso-buildkitd-tls, in the home namespace:
// the CA certificate, the daemon's serving pair, the client pair and an
// "enabled" switch that deploy/buildkitd.yaml's start.sh reads. The CA's
// private key is never stored; rotating means deleting the Secret (Ensure
// mints a fresh set) and restarting the daemon.
//
// Rollout mirrors registryauth: Ensure mints with enabled=false on
// installs that predate this, so nothing changes until an operator flips
// it; fresh installs create the Secret with enabled=true and Ensure fills
// in the certificates while the daemon waits for them. Build Jobs try TLS
// first and fall back to plaintext, so they work against either daemon
// mode and the flip needs no coordination with in-flight builds.
package buildkittls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

const (
	// SecretName holds the daemon's material in the home namespace.
	SecretName = "kuso-buildkitd-tls"
	// ClientSecretName is the per-namespace copy build Jobs mount (CA +
	// client pair only).
	ClientSecretName = "kuso-buildkit-client"
	// ServerName is the name buildctl verifies the daemon certificate
	// against (--tlsservername); every daemon (Deployment and shards)
	// serves the same certificate.
	ServerName = "kuso-buildkitd"

	KeyCA         = "ca.crt"
	KeyServerCert = "tls.crt"
	KeyServerKey  = "tls.key"
	KeyClientCert = "client.crt"
	KeyClientKey  = "client.key"
	keyEnabled    = "enabled"

	validity = 10 * 365 * 24 * time.Hour
)

// Store reads and writes the TLS Secret.
type Store struct {
	Kube      *kube.Client
	Namespace string
}

func (s *Store) ns() string {
	if s.Namespace == "" {
		return "kuso"
	}
	return s.Namespace
}

func complete(d map[string][]byte) bool {
	for _, k := range []string{KeyCA, KeyServerCert, KeyServerKey, KeyClientCert, KeyClientKey} {
		if len(d[k]) == 0 {
			return false
		}
	}
	return true
}

// Ensure makes sure kuso-buildkitd-tls holds a full certificate set. A
// missing Secret is created with enabled=false; an existing one missing
// certificates gets them added with its enabled value kept. Safe on every
// replica: a concurrent create or update fails and the next call sees the
// winner's Secret.
func (s *Store) Ensure(ctx context.Context) error {
	secrets := s.Kube.Clientset.CoreV1().Secrets(s.ns())
	sec, err := secrets.Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	if err == nil && complete(sec.Data) {
		return nil
	}
	set, merr := Mint(time.Now())
	if merr != nil {
		return merr
	}
	if apierrors.IsNotFound(err) {
		set[keyEnabled] = []byte("false")
		_, cerr := secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: s.ns(), Labels: map[string]string{
				"app.kubernetes.io/name": "kuso-buildkitd",
			}},
			Data: set,
		}, metav1.CreateOptions{})
		if cerr != nil && !apierrors.IsAlreadyExists(cerr) {
			return fmt.Errorf("buildkit tls: create secret: %w", cerr)
		}
		return nil
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for k, v := range set {
		sec.Data[k] = v
	}
	if _, uerr := secrets.Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
		return fmt.Errorf("buildkit tls: complete secret: %w", uerr)
	}
	return nil
}

// EnsureLoop runs Ensure until it succeeds, retrying every interval.
func (s *Store) EnsureLoop(ctx context.Context, interval time.Duration, logf func(msg string, args ...any)) {
	for {
		err := s.Ensure(ctx)
		if err == nil {
			return
		}
		if logf != nil {
			logf("buildkit tls: minting kuso-buildkitd-tls failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// EnsureClientSecret mirrors the CA and client pair into ns for build
// Jobs. Returns "" (no error) while no certificates exist; Jobs then
// connect in plaintext, which is what a daemon without TLS expects.
func (s *Store) EnsureClientSecret(ctx context.Context, ns string) (string, error) {
	home, err := s.Kube.Clientset.CoreV1().Secrets(s.ns()).Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	if !complete(home.Data) {
		return "", nil
	}
	want := map[string][]byte{
		KeyCA:         home.Data[KeyCA],
		KeyServerCert: home.Data[KeyClientCert],
		KeyServerKey:  home.Data[KeyClientKey],
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	live, err := secrets.Get(ctx, ClientSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ClientSecretName, Namespace: ns, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
			}},
			Type: corev1.SecretTypeTLS,
			Data: want,
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			err = nil
		}
		if err != nil {
			return "", fmt.Errorf("buildkit tls: create client secret in %s: %w", ns, err)
		}
		return ClientSecretName, nil
	}
	if err != nil {
		return "", fmt.Errorf("buildkit tls: read client secret in %s: %w", ns, err)
	}
	same := true
	for k, v := range want {
		if string(live.Data[k]) != string(v) {
			same = false
		}
	}
	if !same {
		live.Data = want
		if _, err := secrets.Update(ctx, live, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("buildkit tls: update client secret in %s: %w", ns, err)
		}
	}
	return ClientSecretName, nil
}

// ErrDaemonPredatesTLS: the live kuso-buildkitd Deployment doesn't mount
// kuso-buildkitd-tls, so its start.sh can't turn client auth on.
var ErrDaemonPredatesTLS = errors.New("the running kuso-buildkitd predates mutual TLS; apply deploy/buildkitd.yaml from this release first")

// daemonDeployment and shardStatefulSet are the daemons start.sh runs in.
const (
	daemonDeployment = "kuso-buildkitd"
	shardStatefulSet = "kuso-buildkitd-shard"
)

// Enabled reports whether the daemon is set to require client
// certificates. A missing Secret reads as false.
func (s *Store) Enabled(ctx context.Context) (bool, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.ns()).Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	return string(sec.Data[keyEnabled]) == "true", nil
}

// SetEnabled flips client auth on or off and restarts the daemons so
// start.sh picks it up. Build Jobs try TLS first and fall back to
// plaintext, so queued builds survive either direction; builds running
// at the moment of the restart are interrupted, as on any daemon restart.
func (s *Store) SetEnabled(ctx context.Context, enabled bool) error {
	apps := s.Kube.Clientset.AppsV1()
	dep, err := apps.Deployments(s.ns()).Get(ctx, daemonDeployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("buildkit tls: read deployment %s: %w", daemonDeployment, err)
	}
	mounts := false
	for _, v := range dep.Spec.Template.Spec.Volumes {
		if v.Secret != nil && v.Secret.SecretName == SecretName {
			mounts = true
		}
	}
	if !mounts {
		return ErrDaemonPredatesTLS
	}
	if err := s.Ensure(ctx); err != nil {
		return err
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(s.ns())
	sec, err := secrets.Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[keyEnabled] = []byte(strconv.FormatBool(enabled))
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("buildkit tls: update secret: %w", err)
	}
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":%q}}}}}`, time.Now().UTC().Format(time.RFC3339))
	if _, err := apps.Deployments(s.ns()).Patch(ctx, daemonDeployment, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("buildkit tls: restart %s: %w", daemonDeployment, err)
	}
	if _, err := apps.StatefulSets(s.ns()).Patch(ctx, shardStatefulSet, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("buildkit tls: restart %s: %w", shardStatefulSet, err)
	}
	return nil
}

// Mint returns a fresh CA certificate plus server and client pairs signed
// by it, PEM-encoded under the Secret's keys.
func Mint(now time.Time) (map[string][]byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "kuso-buildkitd-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	leaf := func(cn string, usage x509.ExtKeyUsage, dns []string) ([]byte, []byte, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		tmpl := &x509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: cn},
			DNSNames:     dns,
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(validity),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return nil, nil, err
		}
		kder, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
	}
	srvCert, srvKey, err := leaf(ServerName, x509.ExtKeyUsageServerAuth, []string{
		ServerName,
		ServerName + ".kuso.svc.cluster.local",
		"localhost",
	})
	if err != nil {
		return nil, err
	}
	cliCert, cliKey, err := leaf("kuso-build", x509.ExtKeyUsageClientAuth, nil)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		KeyCA:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		KeyServerCert: srvCert,
		KeyServerKey:  srvKey,
		KeyClientCert: cliCert,
		KeyClientKey:  cliKey,
	}, nil
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return n
}
