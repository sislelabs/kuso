package secrets

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// Victim: project "a-b" service "c", Secret "a-b-c-secrets", written before
// secrets were labelled. Attacker: project "a" service "b". The env-scoped
// name for ("a", "b", env "c") is ALSO "a-b-c-secrets".
func envScopeOverlapFixture(t *testing.T, extra ...envSeed) *Service {
	t.Helper()
	seeds := append([]envSeed{
		seedEnv("a-b-production", "a", "b", "production", nil),
		seedEnv("a-b-c-production", "a-b", "c", "production", nil),
	}, extra...)
	s := fakeService(t, seeds...)
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-secrets", Namespace: "kuso"},
		Data:       map[string][]byte{"VICTIM_KEY": []byte("sensitive")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return s
}

func assertVictimUntouched(t *testing.T, s *Service) {
	t.Helper()
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "a-b-c-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("victim secret gone: %v", err)
	}
	if string(sec.Data["VICTIM_KEY"]) != "sensitive" || len(sec.Data) != 1 {
		t.Fatalf("victim secret mutated: %v", sec.Data)
	}
}

func TestEnvScope_UnknownEnvIsRejectedBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	s := envScopeOverlapFixture(t)
	ctx := context.Background()
	if keys, err := s.ListKeys(ctx, "a", "b", "c"); err != nil || len(keys) != 0 {
		t.Errorf("ListKeys(a, b, env=c) = %v, %v; want no keys", keys, err)
	}
	if err := s.SetKey(ctx, "a", "b", "c", "POISON", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetKey(a, b, env=c) = %v, want ErrNotFound", err)
	}
	if err := s.UnsetKey(ctx, "a", "b", "c", "VICTIM_KEY"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UnsetKey(a, b, env=c) = %v, want ErrNotFound", err)
	}
	assertVictimUntouched(t, s)
}

// Even with a real env "c" on the attacker's service (env CR "a-b-c", which
// can coexist with the victim's KusoService "a-b-c"), the derived Secret
// belongs to the victim and must not be read or written.
func TestEnvScope_ForeignSecretIsRejectedEvenForARealEnv(t *testing.T) {
	t.Parallel()
	s := envScopeOverlapFixture(t, seedEnv("a-b-c", "a", "b", "c", nil))
	ctx := context.Background()
	if keys, err := s.ListKeys(ctx, "a", "b", "c"); err != nil || len(keys) != 0 {
		t.Errorf("ListKeys = %v, %v; want no keys", keys, err)
	}
	if err := s.SetKey(ctx, "a", "b", "c", "POISON", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetKey = %v, want ErrNotFound", err)
	}
	assertVictimUntouched(t, s)
}

// Env-delete cleanup derives the per-env Secret from (project, service,
// env). For project "a", service "b-c", env "production" that is project
// "a-b"'s "a-b-c-production-secrets"; a resumed delete must not remove it.
func TestDeleteForEnv_RefusesAnotherProjectsSecret(t *testing.T) {
	t.Parallel()
	s := envScopeOverlapFixture(t)
	ctx := context.Background()
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-production-secrets", Namespace: "kuso"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteForEnv(ctx, "a", "b-c", "production"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteForEnv = %v, want ErrNotFound", err)
	}
	if err := s.DeleteForService(ctx, "a", "b-c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteForService = %v, want ErrNotFound", err)
	}
	for _, name := range []string{"a-b-c-production-secrets", "a-b-c-secrets"} {
		if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatalf("%s deleted: %v", name, err)
		}
	}
}

func TestSetKey_LabelsNewSecretWithProject(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", nil))
	ctx := context.Background()
	if err := s.SetKey(ctx, "alpha", "web", "", "K", "v"); err != nil {
		t.Fatal(err)
	}
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "alpha-web-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Labels[kube.LabelProject] != "alpha" {
		t.Fatalf("labels = %v, want project=alpha", sec.Labels)
	}
}

// CORE-9: attach must edit the LIVE env, not write back the cached list.
// The list reactor serves a stale snapshot taken before an addon refresh
// swapped production's conn for the env's own clone conn.
func TestSetKey_AttachDoesNotRevertAConcurrentEnvFromChange(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnvKindLabel("alpha-web-staging", "alpha", "web", "production", "staging"))
	ctx := context.Background()
	dyn := s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient)

	if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, "kuso", "alpha-web-staging", func(e *kube.KusoEnvironment) error {
		e.Spec.EnvFromSecrets = []string{"alpha-db-staging-conn"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging", Namespace: "kuso", Labels: map[string]string{
			kube.LabelProject: "alpha", kube.LabelService: "web", kube.LabelEnv: "staging",
		}},
		Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Kind: "production",
			EnvFromSecrets: []string{"alpha-db-conn"}},
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&stale)
	if err != nil {
		t.Fatal(err)
	}
	item := unstructured.Unstructured{Object: m}
	item.SetAPIVersion(kube.GVREnvironments.GroupVersion().String())
	item.SetKind("KusoEnvironment")
	dyn.PrependReactor("list", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{item}}, nil
	})

	if err := s.SetKey(ctx, "alpha", "web", "", "K", "v"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	live, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-staging")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha-web-secrets", "alpha-db-staging-conn"}
	if !slices.Equal(live.Spec.EnvFromSecrets, want) {
		t.Fatalf("envFromSecrets = %v, want %v (clone conn kept and last, production conn not resurrected)", live.Spec.EnvFromSecrets, want)
	}
}
