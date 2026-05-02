package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureNamespace creates ns if it doesn't already exist. AlreadyExists
// is treated as success (idempotent). Other errors propagate so callers
// can decide whether to keep going (a hand-pre-created namespace + RBAC
// blocking us is still a working setup).
func (c *Client) EnsureNamespace(ctx context.Context, ns string) error {
	if ns == "" {
		return nil
	}
	_, err := c.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kuso",
			},
		},
	}, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("kube: ensure namespace %q: %w", ns, err)
}
