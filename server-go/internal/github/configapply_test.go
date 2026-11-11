package github

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
	"kuso/server/internal/spec"
)

func TestApplyConfigFromRepo_SkipsWhenNoFile(t *testing.T) {
	// fetch returns (nil, false, nil) for every path → not found.
	called := false
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return nil, false, nil
	}
	apply := func(ctx context.Context, _ *spec.File) error { called = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if called {
		t.Fatal("apply must not run when kuso.yaml is absent")
	}
}

func TestApplyConfigFromRepo_RejectsProjectMismatch(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return []byte("project: other\n"), true, nil
	}
	applied := false
	apply := func(ctx context.Context, _ *spec.File) error { applied = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err == nil {
		t.Fatal("project mismatch must return an error")
	}
	if applied {
		t.Fatal("apply must not run on project mismatch")
	}
}

func TestApplyConfigFromRepo_AppliesMatchingFile(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return []byte("project: proj\n"), true, nil
	}
	var got string
	apply := func(ctx context.Context, f *spec.File) error { got = f.Project; return nil }
	if err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "proj" {
		t.Fatalf("apply was not called with the parsed file: got %q", got)
	}
}

func TestApplyConfigFromRepo_FallsBackToKusoYml(t *testing.T) {
	// kuso.yaml absent, kuso.yml present → fetch is tried for both
	// paths and the .yml content is applied.
	var paths []string
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		paths = append(paths, path)
		if path == "kuso.yml" {
			return []byte("project: proj\n"), true, nil
		}
		return nil, false, nil
	}
	applied := false
	apply := func(ctx context.Context, _ *spec.File) error { applied = true; return nil }
	if err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied {
		t.Fatal("apply must run when kuso.yml is present")
	}
	if len(paths) != 2 || paths[0] != "kuso.yaml" || paths[1] != "kuso.yml" {
		t.Fatalf("expected fetch to try kuso.yaml then kuso.yml, got %v", paths)
	}
}

// SEC-2: a pushed kuso.yaml may only point services at repos the project
// already builds from (or the pushed repo); anything else would get an
// arbitrary App-visible repo cloned into a build the pusher controls.
func TestCheckConfigRepoBinding(t *testing.T) {
	proj := &kube.KusoProject{Spec: kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/org/app"}}}
	svc := unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"repo": map[string]any{"url": "https://github.com/org/Worker.git"}},
	}}
	bound := configBoundRepos(proj, []unstructured.Unstructured{svc}, "org/site")

	for _, repo := range []string{"", "https://github.com/org/app", "https://github.com/org/worker", "org/site", "https://gitlab.com/x/y"} {
		f := &spec.File{Project: "p", Services: []spec.ServiceSpec{{Name: "api", Repo: repo}}}
		if err := checkConfigRepoBinding(f, bound); err != nil {
			t.Errorf("repo %q refused: %v", repo, err)
		}
	}
	f := &spec.File{Project: "p", Services: []spec.ServiceSpec{{Name: "api"}, {Name: "x", Repo: "https://github.com/org/private-b"}}}
	if err := checkConfigRepoBinding(f, bound); !errors.Is(err, ErrConfigRepoNotBound) {
		t.Fatalf("unbound repo: err = %v, want ErrConfigRepoNotBound", err)
	}
}

// SEC-1 (2026-11-10): a push may change how a service runs only when the
// pushed repo builds that service; an image service, or a service on
// another repo, needs `kuso apply` by a project admin.
func TestCheckConfigExecChanges(t *testing.T) {
	proj := &kube.KusoProject{Spec: kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/org/app"}}}
	worker := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-worker"},
		"spec":     map[string]any{"repo": map[string]any{"url": "https://github.com/org/worker"}},
	}}
	tool := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-tool"},
		"spec":     map[string]any{"runtime": "image"},
	}}
	svcs := []unstructured.Unstructured{worker, tool}
	f := &spec.File{Project: "p", Services: []spec.ServiceSpec{{Name: "api"}, {Name: "worker"}, {Name: "tool"}}}
	plan := func(names ...string) *spec.Plan {
		p := &spec.Plan{}
		for _, n := range names {
			p.ExecChanges = append(p.ExecChanges, spec.ExecChange{Resource: "service:" + n, Fields: []string{"command"}})
		}
		return p
	}

	if err := checkConfigExecChanges(plan("api"), f, proj, svcs, "org/app"); err != nil {
		t.Errorf("own-repo command change refused: %v", err)
	}
	if err := checkConfigExecChanges(plan(), f, proj, svcs, "org/app"); err != nil {
		t.Errorf("no exec changes refused: %v", err)
	}
	for _, name := range []string{"worker", "tool"} {
		if err := checkConfigExecChanges(plan(name), f, proj, svcs, "org/app"); !errors.Is(err, ErrConfigExecNotAllowed) {
			t.Errorf("%s: err = %v, want ErrConfigExecNotAllowed", name, err)
		}
	}

	// SEC-3: env changes count on services the pushed repo doesn't build,
	// and no push may wire an existing addon into a service that doesn't
	// use it yet.
	api := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "p-api"},
		"spec":     map[string]any{"subscribedAddons": []any{"cache"}},
	}}
	envSvcs := []unstructured.Unstructured{worker, tool, api}
	envPlan := func(svc, to string, newAddons ...string) *spec.Plan {
		return &spec.Plan{AddonsToCreate: newAddons, Changes: []spec.ResourceChange{{
			Resource: "service:" + svc, Fields: []spec.FieldChange{{Field: "env.X", From: "(unset)", To: to}},
		}}}
	}
	for _, c := range []struct {
		name    string
		plan    *spec.Plan
		refused bool
	}{
		{"own-repo literal", envPlan("api", "(value)"), false},
		{"other-repo literal", envPlan("worker", "(value)"), true},
		{"image service removal", envPlan("tool", "(removed)"), true},
		{"ref to subscribed addon", envPlan("api", "${{ cache.URL }}"), false},
		{"ref to unsubscribed addon", envPlan("api", "${{ db.DATABASE_URL }}"), true},
		{"ref to addon this push creates", envPlan("api", "${{ db.DATABASE_URL }}", "db"), false},
		{"new service with env, other repo", &spec.Plan{ServicesToCreate: []string{"side"}}, true},
	} {
		ff := f
		if c.name == "new service with env, other repo" {
			ff = &spec.File{Project: "p", Services: []spec.ServiceSpec{{Name: "side", Repo: "https://github.com/org/worker",
				Env: map[string]spec.EnvValue{"A": {Value: "1"}}}}}
		}
		err := checkConfigExecChanges(c.plan, ff, proj, envSvcs, "org/app")
		if got := errors.Is(err, ErrConfigExecNotAllowed); got != c.refused {
			t.Errorf("%s: err = %v, want refused=%v", c.name, err, c.refused)
		}
	}
}

func TestConfigPruneAllowed(t *testing.T) {
	if configPruneAllowed(&kube.KusoProject{}) {
		t.Error("prune must be off when configAsCode is unset")
	}
	on := &kube.KusoProject{Spec: kube.KusoProjectSpec{ConfigAsCode: &kube.KusoConfigAsCode{Enabled: true, AllowPrune: true}}}
	if !configPruneAllowed(on) {
		t.Error("allowPrune not honoured")
	}
}
