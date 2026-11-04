package nodejoin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// KnownHostsSecret holds the trust-on-first-use SSH host-key pins. The
// container filesystem is ephemeral, so pins kept in a file were lost on
// every restart/upgrade and the next join trusted any key again.
const KnownHostsSecret = "kuso-nodejoin-known-hosts"

// SecretKnownHosts persists host-key pins in a Secret in the home
// namespace. One data key per host (a hash, since Secret keys can't hold
// ':' from IPv6 / host:port); the value is "host base64(key)" so the
// Secret stays inspectable.
type SecretKnownHosts struct {
	Client    kubernetes.Interface
	Namespace string
}

func knownHostKey(host string) string {
	sum := sha256.Sum256([]byte(host))
	return "h-" + hex.EncodeToString(sum[:16])
}

// Lookup returns the pinned key for host, or nil when none is pinned.
func (s *SecretKnownHosts) Lookup(ctx context.Context, host string) ([]byte, error) {
	sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, KnownHostsSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, ok := sec.Data[knownHostKey(host)]
	if !ok {
		return nil, nil
	}
	h, enc, ok := strings.Cut(string(v), " ")
	if !ok || h != host {
		return nil, fmt.Errorf("known-hosts Secret entry for %s is malformed", host)
	}
	return decodeBase64(enc)
}

// Pin records key for host, retrying on a concurrent write.
func (s *SecretKnownHosts) Pin(ctx context.Context, host string, key []byte) error {
	val := []byte(host + " " + encodeBase64(key))
	for attempt := 0; attempt < 5; attempt++ {
		sec, err := s.Client.CoreV1().Secrets(s.Namespace).Get(ctx, KnownHostsSecret, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = s.Client.CoreV1().Secrets(s.Namespace).Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: KnownHostsSecret, Namespace: s.Namespace},
				Data:       map[string][]byte{knownHostKey(host): val},
			}, metav1.CreateOptions{})
		} else if err == nil {
			if sec.Data == nil {
				sec.Data = map[string][]byte{}
			}
			sec.Data[knownHostKey(host)] = val
			_, err = s.Client.CoreV1().Secrets(s.Namespace).Update(ctx, sec, metav1.UpdateOptions{})
		}
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return fmt.Errorf("pin host key for %s: Secret kept changing underneath us", host)
}

var (
	knownHostsMu    sync.RWMutex
	knownHostsStore *SecretKnownHosts
)

// SetKnownHostStore makes host-key pins persist in a Secret instead of
// the container's ephemeral ~/.ssh/kuso_known_hosts. Called once at boot.
func SetKnownHostStore(s *SecretKnownHosts) {
	knownHostsMu.Lock()
	knownHostsStore = s
	knownHostsMu.Unlock()
}

func currentKnownHostStore() *SecretKnownHosts {
	knownHostsMu.RLock()
	defer knownHostsMu.RUnlock()
	return knownHostsStore
}

// knownHostsTimeout bounds each Secret call made from inside the SSH
// handshake's host-key callback, which has no context of its own.
const knownHostsTimeout = 10 * time.Second
