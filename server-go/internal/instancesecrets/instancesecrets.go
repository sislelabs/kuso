// Package instancesecrets owns the instance-wide shared secret —
// one kube Secret named "kuso-instance-shared" in the kuso ns. The
// kuso server pre-populates every new env's envFromSecrets with
// this name (alongside the project-shared secret + addon conn
// secrets). Use case: third-party API keys you want every service
// in every project to inherit (Sentry DSN, Datadog token, etc.).
//
// Admin-only: the HTTP handler for this package gates writes (and
// the key-list read) behind settings:admin. The auto-attach to env
// happens server-side regardless of caller perms — services boot
// with the keys mounted but can only modify them through the
// /settings/instance-secrets page.
package instancesecrets

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// SecretName is the canonical instance-shared Secret name. Exported
// so projects.services_ops can wire it into envFromSecrets.
const SecretName = "kuso-instance-shared"

type Service struct {
	Kube      *kube.Client
	Namespace string
}

func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

var (
	ErrInvalid = errors.New("instancesecrets: invalid")
)

func (s *Service) ListKeys(ctx context.Context) ([]string, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read instance secret: %w", err)
	}
	out := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		out = append(out, k)
	}
	return out, nil
}

func (s *Service) SetKey(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: key required", ErrInvalid)
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		fresh := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      SecretName,
				Namespace: s.Namespace,
				Labels: map[string]string{
					"kuso.sislelabs.com/role": "instance-shared-envs",
				},
			},
			Data: map[string][]byte{key: []byte(value)},
			Type: corev1.SecretTypeOpaque,
		}
		if _, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Create(ctx, fresh, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create instance secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read instance secret: %w", err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[key] = []byte(value)
	if _, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update instance secret: %w", err)
	}
	return nil
}

func (s *Service) UnsetKey(ctx context.Context, key string) error {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read instance secret: %w", err)
	}
	if sec.Data == nil {
		return nil
	}
	if _, ok := sec.Data[key]; !ok {
		return nil
	}
	delete(sec.Data, key)
	if _, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update instance secret: %w", err)
	}
	return nil
}
