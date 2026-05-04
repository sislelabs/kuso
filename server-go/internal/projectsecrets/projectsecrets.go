// Package projectsecrets implements project-level secret storage —
// env vars that get auto-attached to every service in the project
// via envFromSecrets. Use case: integrations like Resend, Postmark,
// Stripe, OpenAI — anything every service in the same SaaS needs.
//
// Backed by one kube Secret per project named "<project>-shared".
// Created lazily on first SetKey. The kuso-server pre-populates
// every new env's envFromSecrets list to include the shared secret
// alongside the addon connection secrets.
package projectsecrets

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type Service struct {
	Kube       *kube.Client
	Namespace  string
	NSResolver *kube.ProjectNamespaceResolver
}

func New(k *kube.Client, namespace string) *Service {
	if namespace == "" {
		namespace = "kuso"
	}
	return &Service{Kube: k, Namespace: namespace}
}

func (s *Service) nsFor(ctx context.Context, project string) string {
	if s.NSResolver == nil || project == "" {
		return s.Namespace
	}
	return s.NSResolver.NamespaceFor(ctx, project)
}

// SecretName returns the canonical project-shared Secret name.
// Exported because the projects package needs it to wire into
// envFromSecrets.
func SecretName(project string) string {
	return project + "-shared"
}

var (
	ErrInvalid  = errors.New("projectsecrets: invalid")
	ErrNotFound = errors.New("projectsecrets: not found")
)

// ListKeys returns the keys (no values) currently stored. Empty
// slice when the Secret doesn't exist yet.
func (s *Service) ListKeys(ctx context.Context, project string) ([]string, error) {
	sec, err := s.read(ctx, s.nsFor(ctx, project), SecretName(project))
	if err != nil {
		if apierrors.IsNotFound(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read shared secret: %w", err)
	}
	out := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		out = append(out, k)
	}
	return out, nil
}

// SetKey upserts a single env-var-style entry. Creates the Secret
// when it doesn't exist yet.
func (s *Service) SetKey(ctx context.Context, project, key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: key required", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	name := SecretName(project)
	sec, err := s.read(ctx, ns, name)
	if apierrors.IsNotFound(err) {
		// Create fresh.
		fresh := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"kuso.sislelabs.com/project": project,
					"kuso.sislelabs.com/role":    "shared-project-envs",
				},
			},
			Data: map[string][]byte{key: []byte(value)},
			Type: corev1.SecretTypeOpaque,
		}
		_, err := s.Kube.Clientset.CoreV1().Secrets(ns).Create(ctx, fresh, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create shared secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read shared secret: %w", err)
	}
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[key] = []byte(value)
	_, err = s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update shared secret: %w", err)
	}
	return nil
}

// UnsetKey removes one entry. No-op when the key (or the whole
// Secret) doesn't exist — matches the "delete is idempotent"
// expectation for kuso secrets.
func (s *Service) UnsetKey(ctx context.Context, project, key string) error {
	ns := s.nsFor(ctx, project)
	name := SecretName(project)
	sec, err := s.read(ctx, ns, name)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read shared secret: %w", err)
	}
	if sec.Data == nil {
		return nil
	}
	if _, ok := sec.Data[key]; !ok {
		return nil
	}
	delete(sec.Data, key)
	_, err = s.Kube.Clientset.CoreV1().Secrets(ns).Update(ctx, sec, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update shared secret: %w", err)
	}
	return nil
}

func (s *Service) read(ctx context.Context, ns, name string) (*corev1.Secret, error) {
	return s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
}
