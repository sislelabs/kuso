package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// fakeService builds a *Service backed by dynamic/fake. Identical setup
// to the kube package tests; we duplicate the helper rather than export
// a test-only fake from kube to avoid polluting kube's API surface.
func fakeService(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", s.obj.GetName(), err)
		}
	}
	return New(&kube.Client{Dynamic: dyn}, "kuso")
}

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func seedProject(name string, spec kube.KusoProjectSpec) seed {
	return typedSeed(kube.GVRProjects, "KusoProject", name, &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: name},
		},
		Spec: spec,
	})
}

func seedService(project, service string, spec kube.KusoServiceSpec) seed {
	name := serviceCRName(project, service)
	return typedSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				labelProject: project,
				labelService: service,
			},
		},
		Spec: spec,
	})
}

func seedEnv(project, service, kind, branch, name string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels: map[string]string{
				labelProject: project,
				labelService: service,
				labelEnv:     kind,
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project,
			Service: serviceCRName(project, service),
			Kind:    kind,
			Branch:  branch,
		},
	})
}

func typedSeed(gvr schema.GroupVersionResource, kind, name string, obj any) seed {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: kind})
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	if u.GetName() == "" {
		u.SetName(name)
	}
	return seed{gvr: gvr, obj: u}
}

// ---- project ops --------------------------------------------------------

func TestCreate_AppliesDefaults(t *testing.T) {
	t.Parallel()
	s := fakeService(t)

	got, err := s.Create(context.Background(), CreateProjectRequest{
		Name:        "alpha",
		Description: "alpha service",
		DefaultRepo: &CreateProjectRepoSpec{URL: "https://example.com/repo.git"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "alpha" {
		t.Errorf("name: %q", got.Name)
	}
	if got.Spec.DefaultRepo == nil || got.Spec.DefaultRepo.DefaultBranch != "main" {
		t.Errorf("default branch: %+v", got.Spec.DefaultRepo)
	}
	if got.Spec.Previews == nil || got.Spec.Previews.Enabled != false || got.Spec.Previews.TTLDays != 7 {
		t.Errorf("previews defaults: %+v", got.Spec.Previews)
	}
}

func TestCreate_RejectsDuplicate(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x"},
	}))
	_, err := s.Create(context.Background(), CreateProjectRequest{
		Name:        "alpha",
		DefaultRepo: &CreateProjectRepoSpec{URL: "y"},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestCreate_RejectsMissingFields(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	// Name is the only hard requirement now. Empty name → ErrInvalid.
	if _, err := s.Create(context.Background(), CreateProjectRequest{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing name: got %v", err)
	}
}

// As of v0.3.5, defaultRepo is optional — a project is just a
// container, services bring their own repos. Verify a bare-name
// create succeeds and produces a CR with no DefaultRepo.
func TestCreate_NoRepo_OK(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	got, err := s.Create(context.Background(), CreateProjectRequest{Name: "empty"})
	if err != nil {
		t.Fatalf("Create with no repo: %v", err)
	}
	if got.Spec.DefaultRepo != nil {
		t.Errorf("expected nil DefaultRepo, got %+v", got.Spec.DefaultRepo)
	}
}

func TestDescribe_RollsUpAll(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "preview", "feat/x", "alpha-web-pr7"),
		// Cross-project resources should NOT appear in the rollup.
		seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("beta", "api", kube.KusoServiceSpec{Project: "beta"}),
	)
	got, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got.Project.Name != "alpha" {
		t.Errorf("project: %+v", got.Project)
	}
	if len(got.Services) != 2 {
		t.Errorf("services: got %d, want 2", len(got.Services))
	}
	if len(got.Environments) != 2 {
		t.Errorf("envs: got %d, want 2", len(got.Environments))
	}
}

func TestDescribe_NotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	if _, err := s.Describe(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

// TestDescribe_ReflectsMutations verifies that Describe re-reads
// after each mutation. The previous 5s describe cache was removed
// (see A-P1-5): the three list calls inside Describe now go through
// the cached list[T] helper in kube/crds.go, which is informer-
// served, so per-call cost is already O(1) without the explicit
// cache layer. This test asserts the freshness invariant — the
// pointer-equality assertion that used to live here is gone with
// the cache.
func TestDescribe_ReflectsMutations(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	first, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(first.Services) != 1 {
		t.Fatalf("services before AddService: %d, want 1", len(first.Services))
	}
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "api"}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	fresh, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe (fresh): %v", err)
	}
	if len(fresh.Services) != 2 {
		t.Errorf("services after AddService: %d, want 2", len(fresh.Services))
	}
}

func TestDelete_CascadesEnvsAndServices(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if err := s.Delete(context.Background(), "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Project gone.
	if _, err := s.Get(context.Background(), "alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("project still present: %v", err)
	}
	svcs, _ := s.ListServices(context.Background(), "alpha")
	if len(svcs) != 0 {
		t.Errorf("services not cascaded: %+v", svcs)
	}
	envs, _ := s.ListEnvironments(context.Background(), "alpha")
	if len(envs) != 0 {
		t.Errorf("envs not cascaded: %+v", envs)
	}
}

// TestDelete_CleansProjectScopedSecrets is the regression test for the
// orphaned-secret bug: project delete must remove the imperatively-created
// Secrets (<project>-shared, <project>-<svc>-secrets, env-scoped) that no
// helm chart owns. Otherwise they linger in the shared `kuso` namespace and
// a same-named project recreated later inherits the dead project's stale
// (often placeholder) values.
func TestDelete_CleansProjectScopedSecrets(t *testing.T) {
	t.Parallel()

	// Typed clientset seeded with the three orphan-prone Secret shapes.
	cs := k8sfake.NewSimpleClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-shared", Namespace: "kuso",
			Labels: map[string]string{labelProject: "alpha"},
		}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: kube.ServiceSecretName("alpha", "web"), Namespace: "kuso",
		}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: kube.EnvSecretName("alpha", "web", "production"), Namespace: "kuso",
		}},
		// A DIFFERENT project's shared secret must survive (no over-delete).
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: "beta-shared", Namespace: "kuso",
			Labels: map[string]string{labelProject: "beta"},
		}},
	)

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRKuso: "KusoList", kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList", kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons: "KusoAddonList", kube.GVRBuilds: "KusoBuildList",
	})
	for _, sd := range []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	s := New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")

	if err := s.Delete(context.Background(), "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	gone := []string{
		"alpha-shared",
		kube.ServiceSecretName("alpha", "web"),
		kube.EnvSecretName("alpha", "web", "production"),
	}
	for _, n := range gone {
		if _, err := cs.CoreV1().Secrets("kuso").Get(context.Background(), n, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("secret %s should be deleted, got err=%v", n, err)
		}
	}
	// beta's shared secret must remain untouched.
	if _, err := cs.CoreV1().Secrets("kuso").Get(context.Background(), "beta-shared", metav1.GetOptions{}); err != nil {
		t.Errorf("beta-shared should survive alpha's delete, got err=%v", err)
	}
}

// ---- service ops --------------------------------------------------------

func TestAddService_AutoCreatesProductionEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))

	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		Port:    3000,
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Name != "alpha-web" || created.Spec.Project != "alpha" || created.Spec.Port != 3000 {
		t.Errorf("service: %+v", created)
	}

	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("env not auto-created: %v", err)
	}
	if env.Spec.Kind != "production" || env.Spec.Branch != "main" {
		t.Errorf("env spec: %+v", env.Spec)
	}
	if env.Spec.Host != "web.alpha.example.com" {
		t.Errorf("host: got %q", env.Spec.Host)
	}
	if env.Spec.Port != 3000 || env.Spec.ReplicaCountValue() != 1 {
		t.Errorf("port/replicas: %d/%d", env.Spec.Port, env.Spec.ReplicaCountValue())
	}
}

// TestSlugifyServiceName covers the display-name → slug path used by
// AddService when the user types "Todo API" in the dialog. Edge cases:
// leading/trailing whitespace, runs of separators, diacritics dropped,
// emoji-only input → empty.
func TestSlugifyServiceName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Todo API":          "todo-api",
		"  Auth  Service ":  "auth-service",
		"foo--bar":          "foo-bar",
		"foo_bar.baz":       "foo-bar-baz",
		"FOO":               "foo",
		"a/b":               "ab",
		"":                  "",
		"   ":               "",
		"🎉":                 "",
		"-leading":          "leading",
		"trailing-":         "trailing",
		"Todo API v2 Beta!": "todo-api-v2-beta",
	}
	for in, want := range cases {
		if got := SlugifyServiceName(in); got != want {
			t.Errorf("SlugifyServiceName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAddService_DisplayNameSlugifies asserts that a free-form name
// passed in CreateServiceRequest.Name is slugified for the CR + URL,
// while the original input is preserved as the display name.
func TestAddService_DisplayNameSlugifies(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "Todo API",
		Runtime: "dockerfile",
		Port:    8080,
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Name != "alpha-todo-api" {
		t.Errorf("CR name: got %q, want alpha-todo-api", created.Name)
	}
	if created.Spec.DisplayName != "Todo API" {
		t.Errorf("display name: got %q, want %q", created.Spec.DisplayName, "Todo API")
	}
	// Production env's host should track the slug, not the display name.
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-todo-api-production")
	if err != nil {
		t.Fatalf("env not auto-created: %v", err)
	}
	if env.Spec.Host != "todo-api.alpha.example.com" {
		t.Errorf("host: got %q", env.Spec.Host)
	}
}

func TestAddService_RejectsDuplicate(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "web"})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("got %v", err)
	}
}

func TestAddService_ProjectNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	_, err := s.AddService(context.Background(), "ghost", CreateServiceRequest{Name: "web"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestDeleteService_CascadesEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "preview", "feat/x", "alpha-web-pr7"),
	)
	if err := s.DeleteService(context.Background(), "alpha", "web"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	envs, _ := s.ListEnvironments(context.Background(), "alpha")
	if len(envs) != 0 {
		t.Errorf("envs not cascaded: %+v", envs)
	}
}

func TestSetEnv_ReplacesAndRedacts(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			EnvVars: []kube.KusoEnvVar{{Name: "OLD", Value: "old"}},
		}),
	)
	// API_BASE chosen because PORT is now reserved (see
	// TestSetEnv_RejectsReservedNames). Behaviour under test is the
	// same: a plain var round-trips with its value visible.
	err := s.SetEnv(context.Background(), "alpha", "web", []EnvVar{
		{Name: "API_BASE", Value: "https://api.example.com"},
		{Name: "DB_URL", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-web-secrets", "key": "DB_URL"}}},
	})
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	got, err := s.GetEnv(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetEnv: %v", err)
	}
	if len(got) != 2 || got[0].Name != "API_BASE" || got[0].Value != "https://api.example.com" {
		t.Errorf("plain var: %+v", got)
	}
	if got[1].Name != "DB_URL" || got[1].Value != "" || got[1].ValueFrom == nil {
		t.Errorf("secret-backed var should be redacted: %+v", got[1])
	}
}

// TestSetEnv_RejectsReservedNames asserts that PORT / HOSTNAME /
// KUBERNETES_* fail validation rather than silently overriding the
// runtime's port and hostname. PORT was the v0.7.49 demo's cause of
// 502: a postgres POSTGRES_PORT got plumbed into a variable named
// PORT, the API listened on 5432, and the kube Service kept routing
// to 8080. Server-side gate prevents that class of misconfig at the
// boundary regardless of how the var got typed (UI, CLI, API, drag-
// to-connect).
func TestSetEnv_RejectsReservedNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
	}{
		{"PORT"},
		{"HOSTNAME"},
		{"KUBERNETES_SERVICE_HOST"},
		{"KUBERNETES_PORT_443_TCP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeService(t,
				seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
				seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
			)
			err := s.SetEnv(context.Background(), "alpha", "web", []EnvVar{
				{Name: tc.name, Value: "anything"},
			})
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("expected ErrInvalid for %q, got %v", tc.name, err)
			}
		})
	}
}

// TestSetEnv_AllowsLookalikes ensures the reserved-name check is
// exact (not a prefix match for the canonical names) — variables
// like PORT_PUBLIC, HOSTNAME_OVERRIDE, KUBERNETES_VERSION (different
// project entirely) should pass through. KUBERNETES_* is the
// intentional prefix; PORT and HOSTNAME are exact-match only.
func TestSetEnv_AllowsLookalikes(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	allowed := []string{"PORT_PUBLIC", "HOSTNAME_OVERRIDE", "MY_PORT", "PORT2"}
	for _, name := range allowed {
		err := s.SetEnv(context.Background(), "alpha", "web", []EnvVar{
			{Name: name, Value: "ok"},
		})
		if err != nil {
			t.Errorf("expected %q to pass, got %v", name, err)
		}
	}
}

// TestSetEnv_PropagatesToEnvironments verifies that env-var edits saved
// on KusoService also reach owned KusoEnvironments. The kusoenvironment
// helm chart reads only KusoEnvironment.spec.envVars (no merge step
// for service-level vars), so without propagation a SetEnv call saves
// to the service CR but the running pod never sees the change.
func TestSetEnv_PropagatesToEnvironments(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "preview", "feat/x", "alpha-web-pr7"),
	)
	err := s.SetEnv(context.Background(), "alpha", "web", []EnvVar{
		{Name: "API_BASE", Value: "https://api.example.com"},
	})
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	for _, envName := range []string{"alpha-web-production", "alpha-web-pr7"} {
		env, err := s.GetEnvironment(context.Background(), "alpha", envName)
		if err != nil {
			t.Fatalf("GetEnvironment %s: %v", envName, err)
		}
		if len(env.Spec.EnvVars) != 1 || env.Spec.EnvVars[0].Name != "API_BASE" || env.Spec.EnvVars[0].Value != "https://api.example.com" {
			t.Errorf("env %s did not receive propagated envVars: %+v", envName, env.Spec.EnvVars)
		}
	}
}

// TestPatchService_PortPropagatesToEnvironments verifies that a port
// edit on KusoService also rewrites every owned env's spec.port.
// kusoenvironment chart reads only env-CR port for containerPort +
// Service.targetPort, so without propagation the user-visible port
// edit appears to save but never affects the running pod (this is
// what tripped the v0.7.39 demo deploy: PATCH /api/.../web set
// service.port=8080 but env.port stayed at 3000 → Bad Gateway).
func TestPatchService_PortPropagatesToEnvironments(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	newPort := int32(8080)
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Port: &newPort}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if env.Spec.Port != 8080 {
		t.Errorf("env port not propagated: got %d, want 8080", env.Spec.Port)
	}
}

// TestPatchService_DomainsDoNotPropagateToEnvironments asserts that
// editing the service-level spec.domains field DOES NOT touch existing
// envs' AdditionalHosts. v0.16.19 made spec.domains a seed-only
// template — once an env exists, custom domains live exclusively on
// the env CR. The propagation that used to mirror the field caused
// production tab → staging Ingress claiming the same hostname →
// cross-env Ingress conflict.
//
// Per-env edits go through AddEnvDomain / RemoveEnvDomain /
// SetEnvDomains; those write directly to env.Spec.AdditionalHosts and
// are covered by env_domains_test.go.
func TestPatchService_DomainsDoNotPropagateToEnvironments(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	add := []ServiceDomain{
		{Host: "api.example.com", TLS: true},
		{Host: "alt.example.com", TLS: true},
	}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Domains: &add}); err != nil {
		t.Fatalf("PatchService add domains: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	// Env's AdditionalHosts is whatever it was seeded with at create
	// time (empty here); the new service-level domains do NOT leak.
	if len(env.Spec.AdditionalHosts) != 0 {
		t.Errorf("after svc-level PatchService domains: env.AdditionalHosts=%+v, want empty (svc spec.domains is no longer propagated)", env.Spec.AdditionalHosts)
	}
}

// TestPatchService_PrivateEgressPropagatesToEnvironments verifies that
// toggling PrivateEgress on a KusoService also writes the new value
// onto every owned KusoEnvironment. The kusoenvironment chart stamps
// the public-egress pod label off the env CR's spec.privateEgress
// field; a service-level toggle that isn't propagated never reaches a
// running pod (the pod keeps its old label and the network policy stays
// wrong forever).
func TestPatchService_PrivateEgressPropagatesToEnvironments(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		// Service starts with PrivateEgress=false (zero value).
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "preview", "feat/x", "alpha-web-pr7"),
	)
	enable := true
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{PrivateEgress: &enable}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	for _, envName := range []string{"alpha-web-production", "alpha-web-pr7"} {
		env, err := s.GetEnvironment(context.Background(), "alpha", envName)
		if err != nil {
			t.Fatalf("GetEnvironment %s: %v", envName, err)
		}
		if !env.Spec.PrivateEgress {
			t.Errorf("env %s: PrivateEgress not propagated: got false, want true", envName)
		}
	}
}

// TestPatchService_StaticBuildpacksCommand verifies the three
// config-as-code patch fields land on the KusoService CR: a non-nil
// Static/Buildpacks pointer replaces the build config, a non-nil
// Command pointer replaces the run command.
func TestPatchService_StaticBuildpacksCommand(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			Static:  &kube.KusoStaticSpec{BuildCmd: "old build", OutputDir: "old"},
			Command: []string{"old"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	static := &ServiceStaticSpec{BuildCmd: "npm run build", OutputDir: "dist"}
	buildpacks := &ServiceBuildpacksSpec{BuilderImage: "paketobuildpacks/builder:base"}
	command := []string{"./serve", "--port", "8080"}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Static:     static,
		Buildpacks: buildpacks,
		Command:    &command,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	svc, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if svc.Spec.Static == nil || svc.Spec.Static.BuildCmd != "npm run build" || svc.Spec.Static.OutputDir != "dist" {
		t.Errorf("Static not applied: %+v", svc.Spec.Static)
	}
	if svc.Spec.Buildpacks == nil || svc.Spec.Buildpacks.BuilderImage != "paketobuildpacks/builder:base" {
		t.Errorf("Buildpacks not applied: %+v", svc.Spec.Buildpacks)
	}
	if len(svc.Spec.Command) != 3 || svc.Spec.Command[0] != "./serve" {
		t.Errorf("Command not applied: %+v", svc.Spec.Command)
	}

	// A non-nil Command pointer to an empty slice clears the command.
	empty := []string{}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Command: &empty}); err != nil {
		t.Fatalf("PatchService clear command: %v", err)
	}
	svc2, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetService 2: %v", err)
	}
	if len(svc2.Spec.Command) != 0 {
		t.Errorf("Command not cleared: %+v", svc2.Spec.Command)
	}
}

// ---- environment ops ----------------------------------------------------

func TestDeleteEnvironment_RefusesProduction(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-production")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid, got %v", err)
	}
}

func TestDeleteEnvironment_AllowsPreview(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "preview", "feat/x", "alpha-web-pr7"),
	)
	if err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-pr7"); err != nil {
		t.Errorf("DeleteEnvironment: %v", err)
	}
	if _, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-pr7"); !errors.Is(err, ErrNotFound) {
		t.Errorf("env still present: %v", err)
	}
}

func TestGetEnvironment_RejectsCrossProject(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if _, err := s.GetEnvironment(context.Background(), "beta", "alpha-web-production"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for cross-project, got %v", err)
	}
}

func TestUpdate_TogglesPreviews(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x", DefaultBranch: "main"},
		Previews:    &kube.KusoPreviewsSpec{Enabled: false, TTLDays: 7},
	}))
	enable := true
	got, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		Previews: &UpdateProjectPreviewsSpec{Enabled: &enable},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Spec.Previews == nil || !got.Spec.Previews.Enabled {
		t.Errorf("previews not enabled: %+v", got.Spec.Previews)
	}
	if got.Spec.Previews.TTLDays != 7 {
		t.Errorf("ttl bled to zero: %d", got.Spec.Previews.TTLDays)
	}
	if got.Spec.DefaultRepo.URL != "x" || got.Spec.DefaultRepo.DefaultBranch != "main" {
		t.Errorf("default repo was clobbered: %+v", got.Spec.DefaultRepo)
	}
}

func TestUpdate_SetsPreviewsBaseDomain(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x", DefaultBranch: "main"},
		Previews:    &kube.KusoPreviewsSpec{Enabled: true, TTLDays: 7},
	}))
	dom := "tickero.bg"
	got, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		Previews: &UpdateProjectPreviewsSpec{BaseDomain: &dom},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Spec.Previews == nil || got.Spec.Previews.BaseDomain != "tickero.bg" {
		t.Errorf("previews baseDomain not set: %+v", got.Spec.Previews)
	}
	// Setting baseDomain must not clobber enabled/ttl.
	if !got.Spec.Previews.Enabled || got.Spec.Previews.TTLDays != 7 {
		t.Errorf("baseDomain set clobbered other previews fields: %+v", got.Spec.Previews)
	}
}

func TestUpdate_ClearsGitHubInstallation(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x", DefaultBranch: "main"},
		GitHub:      &kube.KusoProjectGithubSpec{InstallationID: 42},
	}))
	got, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		GitHub: &CreateProjectGithubSpec{InstallationID: 0},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Spec.GitHub != nil {
		t.Errorf("expected GitHub binding cleared; got %+v", got.Spec.GitHub)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	if _, err := s.Update(context.Background(), "ghost", UpdateProjectRequest{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
