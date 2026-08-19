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
//
// Every distinct (project, service) referenced by the seeded envs gets
// an owning KusoService CR seeded automatically, so requireOwnedService
// passes on the happy path. Cross-tenant tests seed their victim service
// under the REAL owner and then call with the attacker project — the
// ownership check rejects that even though the Secret exists.
func fakeService(t *testing.T, envSeeds ...envSeed) *Service {
	t.Helper()
	cs := fake.NewSimpleClientset()

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRServices:     "KusoServiceList",
	})
	seededSvc := map[string]bool{}
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
		// Auto-seed the owning KusoService derived from the env's labels.
		proj := e.env.Labels[kube.LabelProject]
		svc := e.env.Labels[kube.LabelService]
		key := proj + "/" + svc
		if proj != "" && svc != "" && !seededSvc[key] {
			seedServiceInto(t, dyn, proj, svc)
			seededSvc[key] = true
		}
	}
	return &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Namespace: "kuso"}
}

// seedServiceInto registers a KusoService CR named "<project>-<service>"
// owned by project into the dynamic tracker.
func seedServiceInto(t *testing.T, dyn *dynamicfake.FakeDynamicClient, project, service string) {
	t.Helper()
	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelService: service,
			},
		},
		Spec: kube.KusoServiceSpec{Project: project},
	}
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(svc)
	if err != nil {
		t.Fatalf("encode service: %v", err)
	}
	obj := &unstructured.Unstructured{Object: u}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVRServices.Group, Version: kube.GVRServices.Version, Kind: "KusoService",
	})
	if err := dyn.Tracker().Create(kube.GVRServices, obj, "kuso"); err != nil {
		t.Fatalf("seed service: %v", err)
	}
}

type envSeed struct {
	env *kube.KusoEnvironment
}

// seedService seeds an owning KusoService onto an already-built fake
// Service, for tests that don't seed any env (so no service is
// auto-derived) but still exercise a guarded secret op.
func seedService(t *testing.T, s *Service, project, service string) {
	t.Helper()
	seedServiceInto(t, s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient), project, service)
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
	seedService(t, s, "alpha", "web")
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

// TestDetachFromAllEnvs_SkipsPreviewEnvs is the symmetric companion to
// TestSetKey_SharedSkipsPreviewEnvs: detach must skip previews so the
// attach/detach surface stays consistent. Today's behaviour is harmless
// (previews never had the shared secret in their EnvFromSecrets), but
// without the skip a future "all envs get the shared secret" tweak
// would silently desync.
func TestDetachFromAllEnvs_SkipsPreviewEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedEnv("alpha-web-production", "alpha", "web", "production", []string{"alpha-web-secrets"}),
		// Preview env carries the secret in its list — we want to
		// confirm detach LEAVES IT alone (because shared secrets are
		// supposed to never get there in the first place; this is a
		// defensive symmetry check, not a real-world setup).
		seedEnv("alpha-web-pr5", "alpha", "web", "preview", []string{"alpha-web-secrets"}),
	)
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
	prod, _ := s.findEnv(context.Background(), "alpha", "web", "production")
	preview, _ := s.findEnv(context.Background(), "alpha", "web", "preview")
	if len(prod.Spec.EnvFromSecrets) != 0 {
		t.Errorf("production should be detached: %+v", prod.Spec.EnvFromSecrets)
	}
	if len(preview.Spec.EnvFromSecrets) != 1 || preview.Spec.EnvFromSecrets[0] != "alpha-web-secrets" {
		t.Errorf("preview should NOT be touched: %+v", preview.Spec.EnvFromSecrets)
	}
}

func TestDeleteForEnv_RemovesSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	// Seed a per-env secret directly. DeleteForEnv must delete it
	// regardless of whether anything is attached — it's the env-delete
	// path's escape hatch for orphans.
	name := Name("alpha", "web", "pr-7")
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"K": []byte("v")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.DeleteForEnv(context.Background(), "alpha", "web", "pr-7"); err != nil {
		t.Fatalf("DeleteForEnv: %v", err)
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), name, metav1.GetOptions{}); err == nil {
		t.Error("secret should be gone after DeleteForEnv")
	}
	// Idempotent — second call is a no-op.
	if err := s.DeleteForEnv(context.Background(), "alpha", "web", "pr-7"); err != nil {
		t.Errorf("second DeleteForEnv should not error on missing: %v", err)
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
		"plain":   "plain",
		"a/b":     "a~1b",
		"a~b":     "a~0b",
		"a/b~c/d": "a~1b~0c~1d",
		"~/":      "~0~1",
	}
	for in, want := range cases {
		if got := jsonPointerEscape(in); got != want {
			t.Errorf("escape(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestMarkGenerated_RoundTrip(t *testing.T) {
	ctx := context.Background()
	s := fakeService(t)
	seedService(t, s, "alpha", "web")
	// The shared Secret must exist before we can annotate it.
	if err := s.SetKey(ctx, "alpha", "web", "", "PAYLOAD_SECRET", "deadbeef"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := s.MarkGenerated(ctx, "alpha", "web", "PAYLOAD_SECRET", "hex32"); err != nil {
		t.Fatalf("MarkGenerated: %v", err)
	}
	// A non-generated hand-set key must NOT appear in GeneratedKinds.
	if err := s.SetKey(ctx, "alpha", "web", "", "OPENAI_API_KEY", "sk-x"); err != nil {
		t.Fatalf("SetKey 2: %v", err)
	}
	kinds, err := s.GeneratedKinds(ctx, "alpha", "web")
	if err != nil {
		t.Fatalf("GeneratedKinds: %v", err)
	}
	if kinds["PAYLOAD_SECRET"] != "hex32" {
		t.Fatalf("want PAYLOAD_SECRET=hex32, got %v", kinds)
	}
	if _, ok := kinds["OPENAI_API_KEY"]; ok {
		t.Fatal("hand-set secret must not be reported as generated")
	}
}

// TestOwnershipGuard_CrossTenant proves that a caller authorized for
// project "foo" cannot read, write, or delete the secrets of a service
// that resolves to the SAME Secret name but is owned by project
// "foo-bar" (name collision: foo + service "bar-svc" == foo-bar +
// service "svc" == "foo-bar-svc-secrets"). Even though the victim's
// Secret and service CR exist, the guard returns ErrNotFound so
// existence is never leaked.
func TestOwnershipGuard_CrossTenant(t *testing.T) {
	ctx := context.Background()
	// Victim: project "foo-bar", service "svc" — Secret name foo-bar-svc-secrets.
	s := fakeService(t, seedEnv("foo-bar-svc-production", "foo-bar", "svc", "production", nil))
	// Seed the victim's live shared secret.
	if err := s.SetKey(ctx, "foo-bar", "svc", "", "VICTIM_KEY", "sensitive"); err != nil {
		t.Fatalf("seed victim secret: %v", err)
	}

	// Attacker is authorized for project "foo" and passes service
	// "bar-svc", which concatenates to the SAME Secret name.
	if _, err := s.ListKeys(ctx, "foo", "bar-svc", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant ListKeys: want ErrNotFound, got %v", err)
	}
	if err := s.SetKeyOpts(ctx, "foo", "bar-svc", "", "POISON", "x", SetOptions{Force: true}); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant SetKey: want ErrNotFound, got %v", err)
	}
	if err := s.UnsetKey(ctx, "foo", "bar-svc", "", "VICTIM_KEY"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-tenant UnsetKey: want ErrNotFound, got %v", err)
	}

	// The victim's Secret must be untouched: VICTIM_KEY present, no POISON.
	sec, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(ctx, "foo-bar-svc-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("victim secret gone: %v", err)
	}
	if string(sec.Data["VICTIM_KEY"]) != "sensitive" {
		t.Errorf("VICTIM_KEY mutated: %q", sec.Data["VICTIM_KEY"])
	}
	if _, ok := sec.Data["POISON"]; ok {
		t.Error("attacker POISON key landed in victim secret")
	}

	// Same-project happy path still works for the real owner.
	if _, err := s.ListKeys(ctx, "foo-bar", "svc", ""); err != nil {
		t.Errorf("owner ListKeys: %v", err)
	}
}

func TestGeneratedKinds_NoClientsetIsGraceful(t *testing.T) {
	// Export may run with a kube.Client lacking secret access — must not panic.
	s := &Service{Kube: &kube.Client{}, Namespace: "kuso"}
	kinds, err := s.GeneratedKinds(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("expected graceful nil, got err: %v", err)
	}
	if len(kinds) != 0 {
		t.Fatalf("expected empty, got %v", kinds)
	}
}

// seedEnvKindLabel builds an env whose chart-semantics Spec.Kind and
// env-GROUP label diverge — the exact shape an env-group CLONE takes
// (clones set Spec.Kind="production" but carry their own env label).
func seedEnvKindLabel(name, project, service, kind, envLabel string) envSeed {
	return envSeed{env: &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     envLabel,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project,
			Service: project + "-" + service,
			Kind:    kind,
		},
	}}
}

// findEnv must resolve by the env-GROUP label, not Spec.Kind. A staging
// clone carries Spec.Kind="production" (chart semantics) — findEnv("production")
// must NOT match it, and findEnv("staging") must.
func TestFindEnv_SelectsByEnvLabelNotSpecKind(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		// true production: kind + label both "production"
		seedEnvKindLabel("alpha-web-production", "alpha", "web", "production", "production"),
		// staging clone: chart-semantics kind="production" but env group "staging"
		seedEnvKindLabel("alpha-web-staging", "alpha", "web", "production", "staging"),
	)
	ctx := context.Background()

	prod, err := s.findEnv(ctx, "alpha", "web", "production")
	if err != nil {
		t.Fatalf("findEnv(production): %v", err)
	}
	if prod.Name != "alpha-web-production" {
		t.Fatalf("findEnv(production) selected %q, want the true production env (a staging clone with kind==production must not match)", prod.Name)
	}

	staging, err := s.findEnv(ctx, "alpha", "web", "staging")
	if err != nil {
		t.Fatalf("findEnv(staging): %v", err)
	}
	if staging.Name != "alpha-web-staging" {
		t.Fatalf("findEnv(staging) selected %q, want alpha-web-staging", staging.Name)
	}
}
