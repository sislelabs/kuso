package github

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

func newDispatcher(t *testing.T, seeds ...seed) *Dispatcher {
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
			t.Fatalf("seed: %v", err)
		}
	}
	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	return NewDispatcher(kc, builds.New(kc, "kuso"), "kuso", slog.Default())
}

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func seedProj(name, repoURL, defaultBranch string, previewsEnabled bool, ttlDays int) seed {
	p := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: repoURL, DefaultBranch: defaultBranch},
			Previews:    &kube.KusoPreviewsSpec{Enabled: previewsEnabled, TTLDays: ttlDays},
			BaseDomain:  name + ".example.com",
		},
	}
	return typedSeed(kube.GVRProjects, "KusoProject", p)
}

func seedSvc(project, service string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": project, "kuso.sislelabs.com/service": service},
		},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/example/" + service, Path: "."},
			Port:    3000,
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func seedPreviewEnv(project, service string, prNumber int, branch string) seed {
	envName := project + "-" + service + "-pr-" + strconv.Itoa(prNumber)
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Kind: "preview", Branch: branch},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func typedSeed(gvr schema.GroupVersionResource, kind string, obj any) seed {
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	return seed{gvr: gvr, obj: u}
}

func TestDispatch_PushOnDefaultBranchTriggersBuild(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"ref": "refs/heads/main",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}
	}`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatalf("Dispatch push: %v", err)
	}
	bs, err := d.Builds.List(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("List builds: %v", err)
	}
	if len(bs) != 1 {
		t.Errorf("expected 1 build triggered, got %d", len(bs))
	}
}

func TestDispatch_PushOnNonDefaultBranchSkips(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"ref": "refs/heads/dev",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}
	}`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatalf("Dispatch push: %v", err)
	}
	bs, _ := d.Builds.List(context.Background(), "alpha", "web")
	if len(bs) != 0 {
		t.Errorf("expected no build (non-default branch), got %d", len(bs))
	}
}

func TestDispatch_PushUnknownRepoSkips(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"ref": "refs/heads/main",
		"repository": {"full_name": "other/repo", "default_branch": "main"}
	}`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	bs, _ := d.Builds.List(context.Background(), "alpha", "web")
	if len(bs) != 0 {
		t.Errorf("unrelated repo triggered build: %d", len(bs))
	}
}

func TestDispatch_PROpened_CreatesPreviewEnvAndBuild(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}
	envName := "alpha-web-pr-42"
	envCR, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", envName)
	if err != nil {
		t.Fatalf("preview env not created: %v", err)
	}
	if envCR.Spec.Kind != "preview" {
		t.Errorf("kind: %q", envCR.Spec.Kind)
	}
	if envCR.Spec.PullRequest == nil || envCR.Spec.PullRequest.Number != 42 {
		t.Errorf("pullRequest: %+v", envCR.Spec.PullRequest)
	}
	if envCR.Spec.Host != "web-pr-42.alpha.example.com" {
		t.Errorf("host: %q", envCR.Spec.Host)
	}
	bs, _ := d.Builds.List(context.Background(), "alpha", "web")
	if len(bs) != 1 {
		t.Errorf("expected 1 build for preview, got %d", len(bs))
	}
}

func TestDispatch_PRClosed_DeletesPreviewEnv(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		seedPreviewEnv("alpha", "web", 42, "feat/x"),
	)

	body := []byte(`{
		"action": "closed",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abc"}, "state": "closed"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr closed: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42"); err == nil {
		t.Error("preview env still present after PR closed")
	}
}

func TestDispatch_PRPreviewsDisabledSkipped(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 0), // previews=false
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 7,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abc"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-7"); err == nil {
		t.Error("preview env created with previews disabled")
	}
}

func TestDispatch_UnknownEvent(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t)
	if err := d.Dispatch(context.Background(), "ping", []byte(`{}`)); err != nil {
		t.Errorf("unknown event should be a no-op, got %v", err)
	}
}

func TestDispatch_BadJSON(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t)
	err := d.Dispatch(context.Background(), "push", []byte(`{not-json`))
	if err == nil || !errors.Is(err, errors.Unwrap(err)) {
		// Just check we got an error — don't pin it on a specific error
		// type since encoding/json's errors are stable.
	}
	if err == nil {
		t.Error("expected error for malformed body")
	}
}
