// Package registryauth manages the credential for the in-cluster
// kuso-registry (review S1 / N6 / B-01 / BLOB-5).
//
// The registry has no "auth optional" mode: once htpasswd is configured
// every pull and push must authenticate. Turning it on before every
// consumer holds the credential would brick every reschedule (kubelet
// pulls), every build (buildkit push) and the retention sweep. So the
// rollout is staged around one Secret, kuso-registry-auth in the home
// namespace:
//
//  1. Ensure mints the credential with enabled=false. From then on every
//     consumer that can read it sends it: the server's registry client,
//     builds (a per-namespace copy, PushSecretName), new nodes (the join
//     bootstrap writes it into registries.yaml). The registry ignores the
//     Authorization header while auth is off, so this step changes nothing.
//  2. Existing nodes get the credential by hand (see deploy/registry.yaml).
//  3. ProbeNodes checks every node's registries.yaml carries the current
//     credential marker; Status.ReadyToEnable is true only when all do.
//  4. The operator sets enabled=true and restarts the registry, whose
//     entrypoint switches htpasswd on. Setting it back to false and
//     restarting reverts.
package registryauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const (
	// SecretName holds the canonical credential in the home namespace.
	SecretName = "kuso-registry-auth"
	// PushSecretName is the per-namespace copy build Jobs mount.
	PushSecretName = "kuso-registry-push"
	// Username is fixed; only the password rotates.
	Username = "kuso"

	keyUsername = "username"
	keyPassword = "password"
	keyHtpasswd = "htpasswd"
	keyEnabled  = "enabled"
	keyDocker   = ".dockerconfigjson"
	keyCNB      = "cnb_registry_auth"
)

var ErrInvalid = errors.New("registry auth: invalid")

// Creds is the decoded credential.
type Creds struct {
	Username string
	Password string
	Enabled  bool
}

// Marker identifies the credential in node registries.yaml files without
// revealing it. It changes when the password rotates, so a node holding
// an old password reads as not ready.
func (c Creds) Marker() string {
	sum := sha256.Sum256([]byte(c.Username + ":" + c.Password))
	return hex.EncodeToString(sum[:8])
}

// BasicAuth is the Authorization header value.
func (c Creds) BasicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password))
}

// DockerConfigJSON is a kubernetes.io/dockerconfigjson payload for host.
func (c Creds) DockerConfigJSON(host string) []byte {
	b, _ := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{
		"username": c.Username, "password": c.Password,
		"auth": base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password)),
	}}})
	return b
}

// CNBAuth is the CNB_REGISTRY_AUTH value the buildpacks lifecycle reads.
func (c Creds) CNBAuth(host string) string {
	b, _ := json.Marshal(map[string]string{host: c.BasicAuth()})
	return string(b)
}

// Store reads and writes the credential. Get is cached briefly because the
// registry client calls it per request.
type Store struct {
	Kube      *kube.Client
	Namespace string
	Host      string

	mu      sync.Mutex
	cached  *Creds
	expires time.Time
}

func (s *Store) ns() string {
	if s.Namespace == "" {
		return "kuso"
	}
	return s.Namespace
}

// Get returns the credential, or nil when none has been minted. Callers
// treat nil as "registry is anonymous" and send nothing.
func (s *Store) Get(ctx context.Context) (*Creds, error) {
	if s == nil || s.Kube == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.expires) {
		return s.cached, nil
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.ns()).Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		s.cached, s.expires = nil, time.Now().Add(time.Minute)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("registry auth: read secret: %w", err)
	}
	c := decode(sec)
	s.cached, s.expires = c, time.Now().Add(time.Minute)
	return c, nil
}

func decode(sec *corev1.Secret) *Creds {
	u, p := string(sec.Data[keyUsername]), string(sec.Data[keyPassword])
	if u == "" || p == "" {
		return nil
	}
	return &Creds{Username: u, Password: p, Enabled: string(sec.Data[keyEnabled]) == "true"}
}

// Ensure mints the credential if absent and returns it. An existing
// credential is returned unchanged: rotating means every node's
// registries.yaml must change first, which is a manual procedure.
func (s *Store) Ensure(ctx context.Context) (*Creds, error) {
	secrets := s.Kube.Clientset.CoreV1().Secrets(s.ns())
	if sec, err := secrets.Get(ctx, SecretName, metav1.GetOptions{}); err == nil {
		if c := decode(sec); c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("%w: %s exists without username/password", ErrInvalid, SecretName)
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("registry auth: read secret: %w", err)
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	// Hex, not base64: the node bootstrap parses its JSON response with
	// sed and cannot unescape quotes or slashes.
	c := &Creds{Username: Username, Password: hex.EncodeToString(raw)}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: SecretName, Namespace: s.ns(), Labels: map[string]string{
			"app.kubernetes.io/name": "kuso-registry",
		}},
		Data: map[string][]byte{
			keyUsername: []byte(c.Username),
			keyPassword: []byte(c.Password),
			keyHtpasswd: []byte(c.Username + ":" + string(hash) + "\n"),
			keyEnabled:  []byte("false"),
			keyDocker:   c.DockerConfigJSON(s.Host),
			keyCNB:      []byte(c.CNBAuth(s.Host)),
		},
	}
	if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("registry auth: create secret: %w", err)
	}
	s.mu.Lock()
	s.expires = time.Time{}
	s.mu.Unlock()
	return c, nil
}

// EnsurePushSecret mirrors the credential into ns for build Jobs. Returns
// "" (no error) when no credential exists, so builds stay anonymous.
func (s *Store) EnsurePushSecret(ctx context.Context, ns string) (string, error) {
	c, err := s.Get(ctx)
	if err != nil || c == nil {
		return "", err
	}
	want := map[string][]byte{
		keyDocker: c.DockerConfigJSON(s.Host),
		keyCNB:    []byte(c.CNBAuth(s.Host)),
	}
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	live, err := secrets.Get(ctx, PushSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: PushSecretName, Namespace: ns, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso-server",
			}},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: want,
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			err = nil
		}
		if err != nil {
			return "", fmt.Errorf("registry auth: create push secret in %s: %w", ns, err)
		}
		return PushSecretName, nil
	}
	if err != nil {
		return "", fmt.Errorf("registry auth: read push secret in %s: %w", ns, err)
	}
	if string(live.Data[keyDocker]) != string(want[keyDocker]) || string(live.Data[keyCNB]) != string(want[keyCNB]) {
		live.Data = want
		if _, err := secrets.Update(ctx, live, metav1.UpdateOptions{}); err != nil {
			return "", fmt.Errorf("registry auth: update push secret in %s: %w", ns, err)
		}
	}
	return PushSecretName, nil
}

// Complete fills the derived keys (htpasswd, docker config, CNB auth) of
// a credential that was written with only username/password/enabled.
// Fresh installs do that: hack/install.sh and the sandbox mint the
// password in shell (where bcrypt isn't available), write it into every
// node's registries.yaml, and create the Secret with enabled=true before
// the registry starts. The registry waits for htpasswd, which this fills
// once kuso-server is up. Returns done=true once nothing is left to fill
// (including when no Secret exists). Safe on every replica: a concurrent
// writer makes Update conflict and the next call sees the filled Secret.
func (s *Store) Complete(ctx context.Context) (done bool, err error) {
	secrets := s.Kube.Clientset.CoreV1().Secrets(s.ns())
	sec, err := secrets.Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("registry auth: read secret: %w", err)
	}
	c := decode(sec)
	if c == nil {
		return true, nil
	}
	if len(sec.Data[keyHtpasswd]) > 0 && len(sec.Data[keyDocker]) > 0 && len(sec.Data[keyCNB]) > 0 {
		return true, nil
	}
	if len(sec.Data[keyHtpasswd]) == 0 {
		hash, herr := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
		if herr != nil {
			return false, herr
		}
		sec.Data[keyHtpasswd] = []byte(c.Username + ":" + string(hash) + "\n")
	}
	sec.Data[keyDocker] = c.DockerConfigJSON(s.Host)
	sec.Data[keyCNB] = []byte(c.CNBAuth(s.Host))
	if _, err := secrets.Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("registry auth: complete secret: %w", err)
	}
	s.mu.Lock()
	s.expires = time.Time{}
	s.mu.Unlock()
	return true, nil
}

// CompleteLoop runs Complete until it reports done, retrying every
// interval. Run once per process at boot.
func (s *Store) CompleteLoop(ctx context.Context, interval time.Duration, logf func(msg string, args ...any)) {
	for {
		done, err := s.Complete(ctx)
		if done {
			return
		}
		if err != nil && logf != nil {
			logf("registry auth: completing kuso-registry-auth failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
