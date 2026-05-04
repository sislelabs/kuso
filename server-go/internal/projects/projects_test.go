package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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

// TestDescribe_CachesAndInvalidates verifies that Describe memoises
// results for the cache TTL and drops them on mutation. Without the
// cache, every projects-index render fans out 3 + 3E kube calls per
// project; with it, the second render of a stable project is O(1).
func TestDescribe_CachesAndInvalidates(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)
	first, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	cached, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe (cached): %v", err)
	}
	// Pointer-equal: the cache should return the same response struct.
	if first != cached {
		t.Errorf("expected cached pointer-equal response, got distinct values")
	}
	// Mutating a service must invalidate the cache so the next
	// Describe re-fetches.
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "api"}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	fresh, err := s.Describe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Describe (fresh): %v", err)
	}
	if fresh == cached {
		t.Errorf("AddService should have invalidated the cache; got the stale entry")
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
	if env.Spec.Port != 3000 || env.Spec.ReplicaCount != 1 {
		t.Errorf("port/replicas: %d/%d", env.Spec.Port, env.Spec.ReplicaCount)
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
	err := s.SetEnv(context.Background(), "alpha", "web", []EnvVar{
		{Name: "PORT", Value: "3000"},
		{Name: "DB_URL", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-web-secrets", "key": "DB_URL"}}},
	})
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	got, err := s.GetEnv(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetEnv: %v", err)
	}
	if len(got) != 2 || got[0].Name != "PORT" || got[0].Value != "3000" {
		t.Errorf("plain var: %+v", got)
	}
	if got[1].Name != "DB_URL" || got[1].Value != "" || got[1].ValueFrom == nil {
		t.Errorf("secret-backed var should be redacted: %+v", got[1])
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
