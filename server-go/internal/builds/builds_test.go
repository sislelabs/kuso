package builds

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func fakeService(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	cs := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRBuilds:       "KusoBuildList",
	})
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, "kuso"); err != nil {
			t.Fatalf("seed %s/%s: %v", s.gvr.Resource, s.obj.GetName(), err)
		}
	}
	return &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Namespace: "kuso"}
}

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func seedProject(name string, defaultBranch, repoURL string, installationID int64) seed {
	p := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: repoURL, DefaultBranch: defaultBranch},
		},
	}
	if installationID > 0 {
		p.Spec.GitHub = &kube.KusoProjectGithubSpec{InstallationID: installationID}
	}
	return typedSeed(kube.GVRProjects, "KusoProject", p)
}

func seedService(project, service string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/example/" + service, Path: "."},
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func seedBuild(b *kube.KusoBuild) seed {
	return typedSeed(kube.GVRBuilds, "KusoBuild", b)
}

func seedProductionEnv(project, service string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service + "-production", Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Kind: "production"},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func typedSeed(gvr schema.GroupVersionResource, kind string, obj any) seed {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	return seed{gvr: gvr, obj: u}
}

// ---- pure helpers --------------------------------------------------------

func TestImageTag(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abcdef0123456789abcdef0123456789abcdef01": "abcdef012345",
		"main-abc":  "main-abc",
		"feat/x":    "feat/x", // not validated for branches
	}
	for in, want := range cases {
		if got := ImageTag(in); got != want {
			t.Errorf("ImageTag(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestShortRef_KubeNameSafe(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abcdef0123456789abcdef0123456789abcdef01": "abcdef012345",
		"feat/x":           "feat-x",
		"FEAT/Long-Branch": "feat-long-branch",
	}
	for in, want := range cases {
		if got := shortRef(in); got != want {
			t.Errorf("shortRef(%q): got %q, want %q", in, got, want)
		}
	}
}

// ---- create --------------------------------------------------------------

func TestCreate_FullSHARef(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "alpha-web-abcdef012345" {
		t.Errorf("name: %q", got.Name)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != "abcdef012345" {
		t.Errorf("image tag: %+v", got.Spec.Image)
	}
	if got.Spec.Image.Repository != "kuso-registry.kuso.svc.cluster.local:5000/alpha/web" {
		t.Errorf("repo: %q", got.Spec.Image.Repository)
	}
	if got.Spec.Strategy != "dockerfile" {
		t.Errorf("strategy: %q", got.Spec.Strategy)
	}
	if got.Labels["kuso.sislelabs.com/build-ref"] != "abcdef012345" {
		t.Errorf("build-ref label: %v", got.Labels)
	}
}

func TestCreate_BranchOnly_SyntheticRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Branch: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Synthetic ref has the form main-<base36-unix-millis>; the prefix
	// is what we assert because the timestamp varies.
	if !strings.HasPrefix(got.Spec.Ref, "main-") {
		t.Errorf("synthetic ref: %q", got.Spec.Ref)
	}
	if got.Spec.Image.Tag != got.Spec.Ref {
		t.Errorf("non-SHA ref should pass through verbatim as tag: %q vs %q", got.Spec.Image.Tag, got.Spec.Ref)
	}
}

func TestCreate_NoServiceErrNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", "main", "https://github.com/x/y", 0))
	_, err := s.Create(context.Background(), "alpha", "ghost", CreateBuildRequest{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestCreate_NoRepoURLErrInvalid(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "", 0),
		typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web", Namespace: "kuso"},
			Spec:       kube.KusoServiceSpec{Project: "alpha"}, // no repo
		}),
	)
	_, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v", err)
	}
}

// ---- list ----------------------------------------------------------------

func TestList_NewestFirst(t *testing.T) {
	t.Parallel()
	now := time.Now()
	older := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha-web-aaa",
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "aaa"},
	}
	newer := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha-web-bbb",
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "bbb"},
	}
	s := fakeService(t, seedBuild(older), seedBuild(newer))

	got, err := s.List(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[0].Name != "alpha-web-bbb" {
		t.Errorf("expected newest first, got %v", []string{got[0].Name, got[1].Name})
	}
}

// ---- poller --------------------------------------------------------------

func TestPoller_PromotesImageOnSuccess(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-abc", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-web",
			Ref:     "abc",
			Image:   &kube.KusoImage{Repository: "registry/alpha/web", Tag: "abc"},
		},
	}
	s := fakeService(t, seedBuild(build), seedProductionEnv("alpha", "web"))
	// Seed a completed Job that mirrors the kaniko output.
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-abc", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: "True"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Build status should be succeeded now.
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-abc")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if got.Status["phase"] != "succeeded" {
		t.Errorf("phase: %v", got.Status)
	}

	// Production env's image should have been patched.
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if envCR.Spec.Image == nil || envCR.Spec.Image.Tag != "abc" {
		t.Errorf("env image not promoted: %+v", envCR.Spec.Image)
	}
}

func TestPoller_MarksFailed(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-fff", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "fff", Image: &kube.KusoImage{}},
	}
	s := fakeService(t, seedBuild(build))
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-fff", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: "True", Message: "kaniko exit 1"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-fff")
	if got.Status["phase"] != "failed" {
		t.Errorf("phase: %v", got.Status)
	}
	if msg, _ := got.Status["message"].(string); !strings.Contains(msg, "kaniko") {
		t.Errorf("message: %v", got.Status["message"])
	}
}

func TestPoller_MarksRunning(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-rrr", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "rrr", Image: &kube.KusoImage{}},
	}
	s := fakeService(t, seedBuild(build))
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-rrr", Namespace: "kuso"},
		Status:     batchv1.JobStatus{Active: 1},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-rrr")
	if got.Status["phase"] != "running" {
		t.Errorf("phase: %v", got.Status)
	}
}

func TestPoller_SkipsTerminal(t *testing.T) {
	t.Parallel()
	// A build already marked succeeded MUST NOT be re-poked — no Job
	// existing should be a no-op, not an error path.
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-old", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "old", Image: &kube.KusoImage{}},
		Status:     map[string]any{"phase": "succeeded"},
	}
	s := fakeService(t, seedBuild(b))
	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}
