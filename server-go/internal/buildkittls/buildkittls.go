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
// Rollout: Ensure mints with enabled=false on installs that predate this;
// fresh installs create the Secret with enabled=true and Ensure fills in
// the certificates while the daemon waits for them. On an upgraded install
// AutoEnable then turns it on by itself (review ISO-1: without it any
// Dockerfile RUN step can drive the daemon over 127.0.0.1), but only once
// the daemon mounts the Secret and no build is in flight, so the restart
// interrupts nothing. Build Jobs try TLS first and fall back to plaintext,
// so they work against either daemon mode. An operator's explicit choice
// through SetEnabled is recorded and never overridden.
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
	"log/slog"
	"math/big"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/buildkit"
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
	// keyOperatorSet marks an "enabled" value chosen through SetEnabled
	// (POST system/buildkit-tls). AutoEnable leaves such a Secret alone.
	keyOperatorSet = "operator-set"

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

// ErrDaemonPredatesTLS: a running kuso-buildkitd pod doesn't mount
// kuso-buildkitd-tls, so its start.sh can't turn client auth on.
var ErrDaemonPredatesTLS = errors.New("the running kuso-buildkitd predates mutual TLS; apply deploy/buildkitd.yaml from this release first")

// ErrNoDaemon: no kuso-buildkitd pod is running to switch.
var ErrNoDaemon = errors.New("no kuso-buildkitd pod is running")

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
// The choice is recorded as the operator's, so AutoEnable won't undo an
// explicit "off".
func (s *Store) SetEnabled(ctx context.Context, enabled bool) error {
	_, err := s.set(ctx, enabled, true)
	return err
}

// daemonPods lists the live build daemons (one per build node, or the
// single Deployment's pod on an install that predates the DaemonSet).
// Reading pods rather than the workload keeps this inside RBAC kuso-server
// already holds on every install.
func (s *Store) daemonPods(ctx context.Context) ([]corev1.Pod, error) {
	list, err := s.Kube.Clientset.CoreV1().Pods(s.ns()).List(ctx, metav1.ListOptions{LabelSelector: buildkit.PodLabel})
	if err != nil {
		return nil, fmt.Errorf("buildkit tls: list daemon pods: %w", err)
	}
	var out []corev1.Pod
	for _, p := range list.Items {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) daemonMountsSecret(ctx context.Context) error {
	pods, err := s.daemonPods(ctx)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return ErrNoDaemon
	}
	for _, p := range pods {
		if !mountsSecret(p.Spec.Volumes) {
			return fmt.Errorf("%w (pod %s)", ErrDaemonPredatesTLS, p.Name)
		}
	}
	return nil
}

func mountsSecret(vols []corev1.Volume) bool {
	for _, v := range vols {
		if v.Secret != nil && v.Secret.SecretName == SecretName {
			return true
		}
	}
	return false
}

// set writes the switch and restarts the daemons. For an automatic
// (non-operator) enable it is a no-op when the switch is already on or an
// operator has chosen, and a lost update race reports changed=false: the
// replica that won restarts the daemon, so it restarts once.
func (s *Store) set(ctx context.Context, enabled, operator bool) (changed bool, err error) {
	if err := s.daemonMountsSecret(ctx); err != nil {
		return false, err
	}
	if err := s.Ensure(ctx); err != nil {
		return false, err
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(s.ns())
	sec, err := secrets.Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	if !operator && (string(sec.Data[keyEnabled]) == "true" || string(sec.Data[keyOperatorSet]) == "true") {
		return false, nil
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[keyEnabled] = []byte(strconv.FormatBool(enabled))
	if operator {
		sec.Data[keyOperatorSet] = []byte("true")
	}
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		if !operator && apierrors.IsConflict(err) {
			return false, nil
		}
		return false, fmt.Errorf("buildkit tls: update secret: %w", err)
	}
	// Deleting the pods restarts every daemon; their controller
	// (DaemonSet, or the older single Deployment) recreates them and
	// start.sh reads the new switch.
	pods, err := s.daemonPods(ctx)
	if err != nil {
		return true, err
	}
	for _, p := range pods {
		if err := s.Kube.Clientset.CoreV1().Pods(s.ns()).Delete(ctx, p.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return true, fmt.Errorf("buildkit tls: restart daemon %s: %w", p.Name, err)
		}
	}
	return true, nil
}

// AutoEnable turns client auth on for an install that predates it, once
// that is safe: the certificates exist, the running daemon mounts them
// (ErrDaemonPredatesTLS otherwise; that needs deploy/buildkitd.yaml
// applied by hand, the updater doesn't roll it) and busy reports no build
// in flight, so the restart cuts no build short and every later build Job
// is rendered with the client certificate. done is true once there is
// nothing left to do: TLS is on, or an operator chose a value. flipped is
// true when this call turned it on and restarted the daemon.
func (s *Store) AutoEnable(ctx context.Context, busy func(context.Context) (bool, error)) (done, flipped bool, err error) {
	if err := s.Ensure(ctx); err != nil {
		return false, false, err
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.ns()).Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		return false, false, fmt.Errorf("buildkit tls: read secret: %w", err)
	}
	if string(sec.Data[keyEnabled]) == "true" || string(sec.Data[keyOperatorSet]) == "true" {
		return true, false, nil
	}
	if err := s.daemonMountsSecret(ctx); err != nil {
		return false, false, err
	}
	inFlight, err := busy(ctx)
	if err != nil {
		return false, false, fmt.Errorf("buildkit tls: list in-flight builds: %w", err)
	}
	if inFlight {
		return false, false, nil
	}
	// changed is false when another replica's write won the race; the
	// next call sees its result.
	changed, err := s.set(ctx, true, false)
	return changed, changed, err
}

// AutoEnableLoop retries AutoEnable every interval until it is done.
func (s *Store) AutoEnableLoop(ctx context.Context, interval time.Duration, busy func(context.Context) (bool, error), logger *slog.Logger) {
	var lastErr string
	for {
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		done, flipped, err := s.AutoEnable(actx, busy)
		cancel()
		if flipped {
			logger.Info("buildkit tls: turned on client auth for kuso-buildkitd; daemon restarting",
				"revert", "kuso api POST system/buildkit-tls -f enabled=false")
		}
		if err != nil && err.Error() != lastErr {
			lastErr = err.Error()
			logger.Warn("buildkit tls: turning on client auth for kuso-buildkitd", "err", err, "done", done)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
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
