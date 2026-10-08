package kube

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// Project "a" with service "b" and project "a-b" with service "c" share the
// namespace; "a-b-c" is project a-b's service even though "a" + "b-c"
// concatenates to it.
func ownedFixture(t *testing.T, secrets ...*corev1.Secret) *Client {
	t.Helper()
	c := fakeClient(t,
		seed(GVRServices, "KusoService", "kuso", "a-b", map[string]any{"project": "a"}),
		seed(GVRServices, "KusoService", "kuso", "a-b-c", map[string]any{"project": "a-b"}),
		seed(GVREnvironments, "KusoEnvironment", "kuso", "a-b-production", map[string]any{"project": "a", "service": "a-b"}),
		seed(GVREnvironments, "KusoEnvironment", "kuso", "a-b-c-production", map[string]any{"project": "a-b", "service": "a-b-c"}),
	)
	cs := k8sfake.NewSimpleClientset()
	for _, s := range secrets {
		if _, err := cs.CoreV1().Secrets("kuso").Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c.Clientset = cs
	return c
}

func TestGetOwnedService(t *testing.T) {
	t.Parallel()
	c := ownedFixture(t)
	ctx := context.Background()
	if _, err := c.GetOwnedService(ctx, "kuso", "a", "a-b-c"); !errors.Is(err, ErrNotOwned) {
		t.Errorf("foreign service: got %v, want ErrNotOwned", err)
	}
	if _, err := c.GetOwnedService(ctx, "kuso", "a-b", "a-b-c"); err != nil {
		t.Errorf("own service: %v", err)
	}
	if _, err := c.GetOwnedService(ctx, "kuso", "a", "a-zz"); !apierrors.IsNotFound(err) || !IsNotFoundOrNotOwned(err) {
		t.Errorf("missing service: got %v, want NotFound", err)
	}
}

func TestGetOwnedEnv(t *testing.T) {
	t.Parallel()
	c := ownedFixture(t)
	ctx := context.Background()
	if _, err := c.GetOwnedEnv(ctx, "kuso", "a", "", "a-b-c-production"); !errors.Is(err, ErrNotOwned) {
		t.Errorf("foreign env: got %v, want ErrNotOwned", err)
	}
	if _, err := c.GetOwnedEnv(ctx, "kuso", "a", "a-b", "a-b-production"); err != nil {
		t.Errorf("own env: %v", err)
	}
	// Right project, wrong service.
	if _, err := c.GetOwnedEnv(ctx, "kuso", "a", "a-other", "a-b-production"); !errors.Is(err, ErrNotOwned) {
		t.Errorf("other service's env: got %v, want ErrNotOwned", err)
	}
}

func TestGetOwnedSecret(t *testing.T) {
	t.Parallel()
	c := ownedFixture(t,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-secrets", Namespace: "kuso"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-production-secrets", Namespace: "kuso"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-secrets", Namespace: "kuso"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-x-secrets", Namespace: "kuso", Labels: map[string]string{LabelProject: "a-b"}}},
	)
	ctx := context.Background()
	for _, name := range []string{"a-b-c-secrets", "a-b-c-production-secrets", "a-x-secrets"} {
		if _, err := c.GetOwnedSecret(ctx, "kuso", "a", name); !errors.Is(err, ErrNotOwned) {
			t.Errorf("%s as project a: got %v, want ErrNotOwned", name, err)
		}
	}
	if _, err := c.GetOwnedSecret(ctx, "kuso", "a", "a-b-secrets"); err != nil {
		t.Errorf("own unlabelled secret: %v", err)
	}
	if _, err := c.GetOwnedSecret(ctx, "kuso", "a-b", "a-b-c-secrets"); err != nil {
		t.Errorf("owner reading its secret: %v", err)
	}
}
