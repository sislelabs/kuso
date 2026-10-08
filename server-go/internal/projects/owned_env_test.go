package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Legacy projects share the "kuso" namespace, and env CR names are
// concatenations: project "a-b" service "c" has env "a-b-c-production",
// which also reads as project "a" + service "b-c". A caller authorized on
// "a" must not be able to act on it.
func overlapEnvSeeds() []seed {
	return []seed{
		seedProject("a", kube.KusoProjectSpec{}),
		seedProject("a-b", kube.KusoProjectSpec{}),
		seedService("a", "b", kube.KusoServiceSpec{Project: "a"}),
		seedService("a-b", "c", kube.KusoServiceSpec{Project: "a-b"}),
		seedEnv("a", "b", "production", "main", "a-b-production"),
		seedEnv("a-b", "c", "production", "main", "a-b-c-production"),
	}
}

func TestDeleteEnvironment_RefusesAnotherProjectsEnv(t *testing.T) {
	t.Parallel()
	tls := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-production-tls", Namespace: "kuso"}}
	s, dyn, cs := newCascadeFixture(t, overlapEnvSeeds(), tls)
	ctx := context.Background()

	for _, force := range []bool{false, true} {
		if err := s.deleteEnvironment(ctx, "a", "a-b-c-production", force); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleteEnvironment(a, a-b-c-production, force=%v) = %v, want ErrNotFound", force, err)
		}
	}
	if _, err := dyn.Resource(kube.GVREnvironments).Namespace("kuso").Get(ctx, "a-b-c-production", metav1.GetOptions{}); err != nil {
		t.Fatalf("victim env deleted: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "a-b-c-production-tls", metav1.GetOptions{}); err != nil {
		t.Fatalf("victim TLS secret deleted: %v", err)
	}
}

// A resumed delete (CR already gone) must not reach a name outside the
// caller's "<project>-" prefix.
func TestDeleteEnvironment_ResumedDeleteStaysInsideProjectPrefix(t *testing.T) {
	t.Parallel()
	tls := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "zz-web-staging-tls", Namespace: "kuso"}}
	s, _, cs := newCascadeFixture(t, overlapEnvSeeds(), tls)
	ctx := context.Background()
	if err := s.DeleteEnvironment(ctx, "a", "zz-web-staging"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteEnvironment = %v, want ErrNotFound", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "zz-web-staging-tls", metav1.GetOptions{}); apierrors.IsNotFound(err) {
		t.Fatal("another project's TLS secret was deleted")
	}
}

func TestWakeServiceEnv_RefusesAnotherProjectsEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t, overlapEnvSeeds()...)
	err := s.WakeServiceEnv(context.Background(), "a", "b", "a-b-c-production")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("WakeServiceEnv(a, b, a-b-c-production) = %v, want ErrNotFound", err)
	}
}
