package handlers

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

type fakeFirstBuild struct{ calls int }

func (f *fakeFirstBuild) CreateForService(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func TestTriggerFirstBuild_SkipFirstBuild(t *testing.T) {
	t.Parallel()
	svc := &kube.KusoService{}
	svc.Name = "demo-api"
	svc.Spec.Runtime = "dockerfile"
	svc.Spec.Repo = &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"}

	fb := &fakeFirstBuild{}
	h := &ProjectsHandler{FirstBuildTrigger: fb}

	got := h.triggerFirstBuild(context.Background(), "demo", svc, true)
	if got == nil || !got.Skipped || got.Triggered {
		t.Fatalf("skip: want {Skipped:true}, got %+v", got)
	}
	if fb.calls != 0 {
		t.Fatalf("skip: build started anyway (%d calls)", fb.calls)
	}

	got = h.triggerFirstBuild(context.Background(), "demo", svc, false)
	if got == nil || !got.Triggered || got.Skipped {
		t.Fatalf("no skip: want {Triggered:true}, got %+v", got)
	}
	if fb.calls != 1 {
		t.Fatalf("no skip: want 1 build, got %d", fb.calls)
	}
}

func TestTriggerFirstBuild_SkipIgnoredForImageRuntime(t *testing.T) {
	t.Parallel()
	svc := &kube.KusoService{}
	svc.Spec.Runtime = "image"
	h := &ProjectsHandler{FirstBuildTrigger: &fakeFirstBuild{}}
	if got := h.triggerFirstBuild(context.Background(), "demo", svc, true); got != nil {
		t.Fatalf("image runtime has nothing to build; want nil, got %+v", got)
	}
}
