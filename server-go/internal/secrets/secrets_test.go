package secrets

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// fakeService builds a *Service backed by typed-fake clientset (for
// Secret ops) and dynamic-fake (for KusoEnvironment patches). The two
// fakes share no state, so the env-CR side has to be seeded explicitly.
func fakeService(t *testing.T, envSeeds ...envSeed) *Service {
	t.Helper()
	cs := fake.NewSimpleClientset()

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	for _, e := range envSeeds {
		u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(e.env)
		if err != nil {
			t.Fatalf("encode env: %v", err)
		}
		obj := &unstructured.Unstructured{Object: u}
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group: kube.GVREnvironments.Group, Version: kube.GVREnvironments.Version, Kind: "KusoEnvironment",
		})
		if err := dyn.Tracker().Create(kube.GVREnvironments, obj, "kuso"); err != nil {
			t.Fatalf("seed env: %v", err)
		}
	}
	return &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Namespace: "kuso"}
}

type envSeed struct {
	env *kube.KusoEnvironment
}

func seedEnv(name, project, service, kind string, envFromSecrets []string) envSeed {
	return envSeed{env: &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     kind,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:        project,
			Service:        project + "-" + service,
			Kind:           kind,
			EnvFromSecrets: envFromSecrets,
		},
	}}
}

func TestName_Scopes(t *testing.T) {
	t.Parallel()
	if got, want := Name("alpha", "web", ""), "alpha-web-secrets"; got != want {
		t.Errorf("shared: got %q want %q", got, want)
	}
	if got, want := Name("alpha", "web", "production"), "alpha-web-production-secrets"; got != want {
		t.Errorf("scoped: got %q want %q", got, want)
	}
	if got, want := Name("alpha", "web", "Preview-PR/42"), "alpha-web-preview-pr-42-secrets"; got != want {
		t.Errorf("sanitised: got %q want %q", got, want)
	}
}

func TestSetKey_FirstWriteCreatesSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", nil))
	ctx := context.Background()

	if err := s.SetKey(ctx, "alpha", "web", "", "DB_URL", "postgres://x"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}

	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "alpha-web-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(sec.Data["DB_URL"]) != "postgres://x" {
		t.Errorf("value not persisted: %q", sec.Data["DB_URL"])
	}

	// Env should now have the shared secret attached and a non-empty
	// secretsRev.
	envCR, _ := s.findEnv(ctx, "alpha", "web", "production")
	if len(envCR.Spec.EnvFromSecrets) != 1 || envCR.Spec.EnvFromSecrets[0] != "alpha-web-secrets" {
		t.Errorf("envFromSecrets: %+v", envCR.Spec.EnvFromSecrets)
	}
	if envCR.Spec.SecretsRev == "" {
		t.Errorf("secretsRev not bumped")
	}
}

func TestSetKey_AdditiveOnExisting(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", []string{"alpha-web-secrets"}))
	// Seed the Secret directly so we can test the merge-patch path.
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-secrets", Namespace: "kuso"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"FIRST": []byte("one")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	if err := s.SetKey(context.Background(), "alpha", "web", "", "SECOND", "two"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	sec, _ := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-secrets", metav1.GetOptions{})
	if got := string(sec.Data["FIRST"]); got != "one" {
		t.Errorf("FIRST clobbered: %q", got)
	}
	if got := string(sec.Data["SECOND"]); got != "two" {
		t.Errorf("SECOND missing: %q", got)
	}
}

// TestSetKey_Concurrent_DifferentKeys is the §6.4 regression probe.
//
// The TS fix guarantees: two parallel SetKey calls with different keys
// must both land. The Go port uses the same merge-patch semantics, so
// this test asserts the same invariant against the typed-fake clientset.
//
// (Note: client-go's fake doesn't simulate true concurrency at the
// kube-API level — operations serialise through a tracker mutex — but
// the test still verifies the merge-patch shape produces additive
// outputs, which is the behaviour we rely on in production.)
func TestSetKey_Concurrent_DifferentKeys(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", nil))

	keys := []string{"A", "B", "C", "D", "E", "F"}
	var wg sync.WaitGroup
	wg.Add(len(keys))
	errs := make(chan error, len(keys))
	for _, k := range keys {
		k := k
		go func() {
			defer wg.Done()
			if err := s.SetKey(context.Background(), "alpha", "web", "", k, "value-"+k); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("SetKey: %v", err)
	}

	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := make([]string, 0, len(sec.Data))
	for k, v := range sec.Data {
		if string(v) != "value-"+k {
			t.Errorf("key %s value mismatch: %q", k, v)
		}
		got = append(got, k)
	}
	sort.Strings(got)
	if len(got) != len(keys) {
		t.Errorf("missing keys: got %v, want %v", got, keys)
	}
}

func TestUnsetKey_RemovesAndCascades(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", []string{"alpha-web-secrets"}))
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-secrets", Namespace: "kuso"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"K": []byte("v")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.UnsetKey(context.Background(), "alpha", "web", "", "K"); err != nil {
		t.Fatalf("UnsetKey: %v", err)
	}
	// Secret deleted (last key).
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-secrets", metav1.GetOptions{}); err == nil {
		t.Error("secret should be deleted after last key removed")
	}
	// Env detached.
	envCR, _ := s.findEnv(context.Background(), "alpha", "web", "production")
	if len(envCR.Spec.EnvFromSecrets) != 0 {
		t.Errorf("envFromSecrets not detached: %+v", envCR.Spec.EnvFromSecrets)
	}
}

func TestUnsetKey_PartialRemoveKeepsOthers(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", []string{"alpha-web-secrets"}))
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-secrets", Namespace: "kuso"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"A": []byte("1"), "B": []byte("2")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.UnsetKey(context.Background(), "alpha", "web", "", "A"); err != nil {
		t.Fatalf("UnsetKey: %v", err)
	}
	sec, _ := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-secrets", metav1.GetOptions{})
	if _, ok := sec.Data["A"]; ok {
		t.Error("A still present")
	}
	if string(sec.Data["B"]) != "2" {
		t.Errorf("B clobbered: %q", sec.Data["B"])
	}
}

func TestUnsetKey_MissingKey(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedEnv("alpha-web-production", "alpha", "web", "production", nil))
	err := s.UnsetKey(context.Background(), "alpha", "web", "", "NEVER")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestListKeys_EmptySecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	keys, err := s.ListKeys(context.Background(), "alpha", "web", "")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if keys == nil || len(keys) != 0 {
		t.Errorf("got %v, want empty slice", keys)
	}
}

// TestSetKey_SharedSkipsPreviewEnvs locks in the rule that shared
// secrets attach to non-preview envs only. Previews must boot empty
// so reviewers don't get production credentials in a throwaway URL,
// and so the URL itself is safe to share.
func TestSetKey_SharedSkipsPreviewEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedEnv("alpha-web-production", "alpha", "web", "production", nil),
		seedEnv("alpha-web-pr7", "alpha", "web", "preview", nil),
	)
	if err := s.SetKey(context.Background(), "alpha", "web", "", "DB_URL", "postgres://x"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	prod, _ := s.findEnv(context.Background(), "alpha", "web", "production")
	preview, _ := s.findEnv(context.Background(), "alpha", "web", "preview")
	if len(prod.Spec.EnvFromSecrets) != 1 || prod.Spec.EnvFromSecrets[0] != "alpha-web-secrets" {
		t.Errorf("production should be attached: %+v", prod.Spec.EnvFromSecrets)
	}
	if len(preview.Spec.EnvFromSecrets) != 0 {
		t.Errorf("preview should NOT inherit shared secret: %+v", preview.Spec.EnvFromSecrets)
	}
}

func TestSetKey_PerEnvAttachOnlyThatEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedEnv("alpha-web-production", "alpha", "web", "production", nil),
		seedEnv("alpha-web-pr7", "alpha", "web", "preview", nil),
	)
	if err := s.SetKey(context.Background(), "alpha", "web", "preview", "K", "v"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	prod, _ := s.findEnv(context.Background(), "alpha", "web", "production")
	preview, _ := s.findEnv(context.Background(), "alpha", "web", "preview")

	if len(prod.Spec.EnvFromSecrets) != 0 {
		t.Errorf("production should not be attached for env-scoped write: %+v", prod.Spec.EnvFromSecrets)
	}
	if len(preview.Spec.EnvFromSecrets) != 1 || preview.Spec.EnvFromSecrets[0] != "alpha-web-preview-secrets" {
		t.Errorf("preview attach: %+v", preview.Spec.EnvFromSecrets)
	}
}

func TestJSONPointerEscape(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"plain":      "plain",
		"a/b":        "a~1b",
		"a~b":        "a~0b",
		"a/b~c/d":    "a~1b~0c~1d",
		"~/":         "~0~1",
	}
	for in, want := range cases {
		if got := jsonPointerEscape(in); got != want {
			t.Errorf("escape(%q): got %q, want %q", in, got, want)
		}
	}
}
