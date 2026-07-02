package github

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
	return seedSvcWithDomains(project, service, nil)
}

func seedSvcWithDomains(project, service string, domains []kube.KusoDomain) seed {
	// No per-service repo → the service inherits the project's
	// defaultRepo (the common single-repo-project shape). Multi-repo
	// tests use seedSvcWithRepo to pin a distinct repo.
	return seedSvcWithRepo(project, service, "", domains)
}

func seedSvcWithRepo(project, service, repoURL string, domains []kube.KusoDomain) seed {
	var repo *kube.KusoRepoRef
	if repoURL != "" {
		repo = &kube.KusoRepoRef{URL: repoURL, Path: "."}
	}
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": project, "kuso.sislelabs.com/service": service},
		},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    repo,
			Port:    3000,
			Domains: domains,
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

// --- multi-repo project routing (per-service repo matching) ----------

// A project with two services on two different repos: a push to repo A
// must build ONLY service A, not service B.
func TestDispatch_PushMultiRepo_BuildsOnlyMatchingService(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		// Project default repo = the "api" repo; "web" overrides to its own.
		seedProj("alpha", "https://github.com/example/api", "main", false, 0),
		seedSvcWithRepo("alpha", "web", "https://github.com/example/web", nil),
		seedSvc("alpha", "api"), // inherits example/api
	)
	// Push to the web repo.
	body := []byte(`{
		"ref": "refs/heads/main",
		"repository": {"full_name": "example/web", "default_branch": "main"}
	}`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatalf("Dispatch push: %v", err)
	}
	webBuilds, _ := d.Builds.List(context.Background(), "alpha", "web")
	apiBuilds, _ := d.Builds.List(context.Background(), "alpha", "api")
	if len(webBuilds) != 1 {
		t.Errorf("web (repo matched) should have 1 build, got %d", len(webBuilds))
	}
	if len(apiBuilds) != 0 {
		t.Errorf("api (different repo) should NOT build on a web-repo push, got %d", len(apiBuilds))
	}
}

// A service whose repo lives only on the service (project defaultRepo
// empty) must still build on a push to that repo. This is the nev-abrom
// shape — pre-fix it matched nothing because routing only looked at the
// project default.
func TestDispatch_PushServiceRepoNoProjectDefault_Builds(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("nevabrom", "", "", false, 0), // no project default repo
		seedSvcWithRepo("nevabrom", "site", "https://github.com/biznesguys/nev-abrom", nil),
	)
	body := []byte(`{
		"ref": "refs/heads/main",
		"repository": {"full_name": "biznesguys/nev-abrom", "default_branch": "main"}
	}`)
	if err := d.Dispatch(context.Background(), "push", body); err != nil {
		t.Fatalf("Dispatch push: %v", err)
	}
	bs, _ := d.Builds.List(context.Background(), "nevabrom", "site")
	if len(bs) != 1 {
		t.Errorf("service-repo push (no project default) should build, got %d", len(bs))
	}
}

// A PR on repo A in a two-repo project must spin up a preview only for
// service A, not service B (which tracks repo B).
func TestDispatch_PRMultiRepo_PreviewsOnlyMatchingService(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/api", "main", true, 5),
		seedSvcWithRepo("alpha", "web", "https://github.com/example/web", nil),
		seedSvc("alpha", "api"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 9,
		"pull_request": {"head": {"ref": "feat/x", "sha": "1111111111111111111111111111111111111111"}, "base": {"ref": "main"}, "state": "open"},
		"repository": {"full_name": "example/web"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pull_request: %v", err)
	}
	// web preview env should exist; api should not.
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-9"); err != nil {
		t.Errorf("web preview env should exist for a web-repo PR: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-9"); err == nil {
		t.Error("api preview env should NOT exist for a web-repo PR (different repo)")
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

// TestDispatch_PROpened_PreviewsBaseDomain is the regression test for the
// custom preview-domain feature: when previews.baseDomain is set, preview
// hosts derive from it (web-pr-42.tickero.bg) instead of the project's
// baseDomain (web-pr-42.alpha.example.com).
func TestDispatch_PROpened_PreviewsBaseDomain(t *testing.T) {
	t.Parallel()
	proj := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/example/alpha", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
			// previews pinned to a custom base domain.
			Previews: &kube.KusoPreviewsSpec{Enabled: true, TTLDays: 5, BaseDomain: "tickero.bg"},
		},
	}
	d := newDispatcher(t,
		typedSeed(kube.GVRProjects, "KusoProject", proj),
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
	envCR, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("preview env not created: %v", err)
	}
	if envCR.Spec.Host != "web-pr-42.tickero.bg" {
		t.Errorf("preview host should use previews.baseDomain, got %q (want web-pr-42.tickero.bg)", envCR.Spec.Host)
	}
}

// TestDispatch_PROpened_MarksRewrittenLiteralsAsOverrides is the
// regression test for Bug 5 from the EnvOverrides review: a preview env
// clones the production env's literal envVars and rewrites their hosts
// to the per-PR host (NEXT_PUBLIC_API_URL: api.alpha.example.com →
// api-pr-42.alpha.example.com). Those rewritten literals are deliberate
// per-preview values — they MUST be marked in spec.EnvOverrides so a
// later service-level propagation (extractEnvOnlyOverrides) preserves
// them instead of re-stamping the production value. Without the marker
// the preview pod's browser would call the PRODUCTION API from a preview
// page (CSP blocks it / wrong cookies) the moment any service env edit
// fires propagation.
func TestDispatch_PROpened_MarksRewrittenLiteralsAsOverrides(t *testing.T) {
	t.Parallel()

	prodEnvWeb := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-production",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "web",
				"kuso.sislelabs.com/env":     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha",
			Service: "alpha-web",
			Kind:    "production",
			// A plain LITERAL envVar (not a secretKeyRef) — this is the
			// path rewriteEnvVarValues touches and the one Bug 5 hits.
			EnvVars: []kube.KusoEnvVar{
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.alpha.example.com"},
			},
		},
	}

	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		// "api" service so the hostRewrite map carries an
		// api.alpha.example.com → api-pr-42 entry.
		seedSvc("alpha", "web"),
		seedSvc("alpha", "api"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", prodEnvWeb),
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

	envCR, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("preview env not created: %v", err)
	}

	// The literal must have been rewritten to the preview host...
	var gotVal string
	for _, e := range envCR.Spec.EnvVars {
		if e.Name == "NEXT_PUBLIC_API_URL" {
			gotVal = e.Value
		}
	}
	if gotVal != "https://api-pr-42.alpha.example.com" {
		t.Fatalf("NEXT_PUBLIC_API_URL not rewritten: got %q", gotVal)
	}

	// ...AND the name must be marked as a per-env override so service
	// propagation can't wipe it back to the production value.
	if !slices.Contains(envCR.Spec.EnvOverrides, "NEXT_PUBLIC_API_URL") {
		t.Errorf("NEXT_PUBLIC_API_URL not in EnvOverrides %v — service propagation will re-stamp the production URL onto this preview (Bug 5)", envCR.Spec.EnvOverrides)
	}
}

// seedSvcWithSubscription seeds a service with an explicit subscribedAddons
// list (non-nil = only those addons' conns mount).
func seedSvcWithSubscription(project, service string, subscribedAddons []string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": project, "kuso.sislelabs.com/service": service},
		},
		Spec: kube.KusoServiceSpec{
			Project:          project,
			Port:             3000,
			SubscribedAddons: subscribedAddons,
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func connsOnly(envFrom []string) []string {
	var out []string
	for _, s := range envFrom {
		if strings.HasSuffix(s, "-conn") {
			out = append(out, s)
		}
	}
	return out
}

// TestDispatch_PROpened_RespectsSubscribedAddons is the regression test for
// the preview-env over-mount bug: a preview env must mount only the addon
// conn secrets the parent service subscribes to — NOT every project addon.
// frontend (subscribes to nothing) must get zero addon conns; backoffice
// (storage only) gets storage; api (all) gets all.
func TestDispatch_PROpened_RespectsSubscribedAddons(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvcWithSubscription("alpha", "frontend", []string{}),            // none
		seedSvcWithSubscription("alpha", "backoffice", []string{"storage"}), // storage only
		seedSvcWithSubscription("alpha", "api", []string{"cache", "db", "queue", "storage"}),
	)
	// Stub the project's addon-conn list (4 addons).
	d.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-cache-conn", "alpha-db-conn", "alpha-queue-conn", "alpha-storage-conn"}, nil
	}
	body := []byte(`{
		"action": "opened",
		"number": 7,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	cases := map[string][]string{
		"frontend":   {}, // no addon conns
		"backoffice": {"alpha-storage-conn"},
		"api":        {"alpha-cache-conn", "alpha-db-conn", "alpha-queue-conn", "alpha-storage-conn"},
	}
	for svc, want := range cases {
		env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-"+svc+"-pr-7")
		if err != nil {
			t.Fatalf("get %s preview env: %v", svc, err)
		}
		got := connsOnly(env.Spec.EnvFromSecrets)
		gotSet := map[string]bool{}
		for _, g := range got {
			gotSet[g] = true
		}
		if len(got) != len(want) {
			t.Errorf("%s-pr-7 addon conns = %v, want %v", svc, got, want)
			continue
		}
		for _, w := range want {
			if !gotSet[w] {
				t.Errorf("%s-pr-7 missing expected conn %q (got %v)", svc, w, got)
			}
		}
	}
}

// seedSucceededBuild seeds a terminal (done) succeeded KusoBuild with an
// image, as left behind after a first PR open.
func seedSucceededBuild(project, service, sha, repo, tag string) seed {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-" + sha[:12],
			Namespace: "kuso",
			Annotations: map[string]string{
				"kuso.sislelabs.com/build-phase": "succeeded",
			},
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: project, Service: project + "-" + service, Ref: sha,
			Done:  true,
			Image: &kube.KusoImage{Repository: repo, Tag: tag},
		},
	}
	return typedSeed(kube.GVRBuilds, "KusoBuild", b)
}

// TestDispatch_PRReopened_StampsExistingBuildImage is the regression test
// for the close→reopen promote-gap: a recreated preview env whose SHA-keyed
// build already succeeded must get the existing image stamped (no
// InvalidImageName, no manual build deletion).
func TestDispatch_PRReopened_StampsExistingBuildImage(t *testing.T) {
	t.Parallel()
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		seedSucceededBuild("alpha", "web", sha, "registry/alpha/web", sha[:12]),
	)
	body := []byte(`{
		"action": "reopened",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "` + sha + `"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != sha[:12] {
		t.Errorf("preview env image not stamped from existing build: %+v", env.Spec.Image)
	}
}

// TestDispatch_PROpened_NoExistingBuild_LeavesImageEmpty is the negative
// control: a genuine first open (no prior build) must NOT stamp anything —
// the normal build trigger handles it.
func TestDispatch_PROpened_NoExistingBuild_LeavesImageEmpty(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 43,
		"pull_request": {"head": {"ref": "feat/y", "sha": "1111111111111111111111111111111111111111"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env, _ := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-43")
	if env.Spec.Image != nil {
		t.Errorf("first-open preview env should have empty image (trigger builds it), got %+v", env.Spec.Image)
	}
}

// TestDispatch_PROpened_PreviewIsSingleReplica is the regression test for
// the replica fix: a preview env is always 1 replica with NO autoscaling,
// even when the base/production service carries an HPA-producing scale.
func TestDispatch_PROpened_PreviewIsSingleReplica(t *testing.T) {
	t.Parallel()
	// Service with a scale that would produce an HPA (min 2, max 5).
	min2 := 2
	svc := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web",
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": "alpha", "kuso.sislelabs.com/service": "web"},
		},
		Spec: kube.KusoServiceSpec{
			Project: "alpha",
			Port:    3000,
			Scale:   &kube.KusoScaleSpec{Min: &min2, Max: 5},
		},
	}
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		typedSeed(kube.GVRServices, "KusoService", svc),
	)
	body := []byte(`{
		"action": "opened",
		"number": 44,
		"pull_request": {"head": {"ref": "feat/z", "sha": "2222222222222222222222222222222222222222"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-44")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.ReplicaCount == nil || *env.Spec.ReplicaCount != 1 {
		t.Errorf("preview replicaCount should be 1, got %v", env.Spec.ReplicaCount)
	}
	if env.Spec.Autoscaling != nil {
		t.Errorf("preview must have NO autoscaling, got %+v", env.Spec.Autoscaling)
	}
}

// seedSvcWithRelease seeds a service carrying a release hook (e.g. a
// `migrate up` command) so preview-env propagation of spec.release can
// be asserted.
func seedSvcWithRelease(project, service string, command []string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service,
			Namespace: "kuso",
			Labels:    map[string]string{"kuso.sislelabs.com/project": project, "kuso.sislelabs.com/service": service},
		},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Port:    3000,
			Release: &kube.KusoReleaseSpec{Command: command},
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

// TestDispatch_PROpened_PropagatesReleaseHook is the regression test for the
// preview-migration bug: a PR that adds a DB migration boots its preview
// against the cloned-but-un-migrated schema and 500s. The fix carries the
// parent service's spec.release onto the preview env so the release Job
// (migrations) runs against the preview DB before promote.
func TestDispatch_PROpened_PropagatesReleaseHook(t *testing.T) {
	t.Parallel()
	cmd := []string{"sh", "-c", "migrate up"}
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvcWithRelease("alpha", "api", cmd),
	)
	body := []byte(`{
		"action": "opened",
		"number": 51,
		"pull_request": {"head": {"ref": "feat/migrate", "sha": "5151515151515151515151515151515151515151"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-51")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Release == nil {
		t.Fatalf("preview env should carry the parent service's release hook, got nil")
	}
	if got := env.Spec.Release.Command; !reflect.DeepEqual(got, cmd) {
		t.Errorf("release command: got %v, want %v", got, cmd)
	}
}

// TestDispatch_PROpened_NoReleaseHook_LeavesReleaseNil confirms a service
// without a release hook yields a preview with no release (no-op, unchanged
// behaviour — most services don't migrate).
func TestDispatch_PROpened_NoReleaseHook_LeavesReleaseNil(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 52,
		"pull_request": {"head": {"ref": "feat/x", "sha": "5252525252525252525252525252525252525252"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-52")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Release != nil {
		t.Errorf("preview env should have nil release for a service without a release hook, got %+v", env.Spec.Release)
	}
}

// TestDispatch_PRSynced_RestampsReleaseHook guards the update path: a later
// push to an existing preview PR must re-stamp the release hook (the update
// branch otherwise only carries over EnvFromSecrets, which would drop the
// release on resync and silently regress migrations).
func TestDispatch_PRSynced_RestampsReleaseHook(t *testing.T) {
	t.Parallel()
	cmd := []string{"sh", "-c", "migrate up"}
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvcWithRelease("alpha", "api", cmd),
		// Pre-existing preview env with NO release (simulates one created
		// before the fix, or a bare resync target).
		seedPreviewEnv("alpha", "api", 53, "feat/migrate"),
	)
	body := []byte(`{
		"action": "synchronize",
		"number": 53,
		"pull_request": {"head": {"ref": "feat/migrate", "sha": "5353535353535353535353535353535353535353"}, "state": "open"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-53")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Release == nil {
		t.Fatalf("resynced preview env should re-stamp the release hook, got nil")
	}
	if got := env.Spec.Release.Command; !reflect.DeepEqual(got, cmd) {
		t.Errorf("release command after resync: got %v, want %v", got, cmd)
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

// TestDispatch_PROpened_LegacyMode_InheritsFromProduction locks in
// the v0.17.1 fix for the preview-env env-var inheritance bug.
//
// Symptom: tickero PR-35's api preview env spawned with envVars=null
// even though production carried 11 valueFrom-expanded subscribed
// keys. The api pod crashlooped because JWT_SECRET / EPAY_SECRET etc.
// were never available as explicit env: entries (only via envFrom
// shared blanket-mount, which the app didn't read).
//
// Root cause: dispatcher.go set `baseEnv = ""` whenever the project
// had no previews.triggers[] configured (legacy mode). ensurePreviewEnv
// then skipped the entire baseEnvVars clone block. The fix defaults
// baseEnv to "production" in legacy mode so previews always inherit.
//
// This test seeds a production env with one envVar, fires PR opened
// against a project with NO triggers, and asserts the preview env CR
// carries that envVar through.
func TestDispatch_PROpened_LegacyMode_InheritsFromProduction(t *testing.T) {
	t.Parallel()

	prodEnv := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-production",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "web",
				"kuso.sislelabs.com/env":     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha",
			Service: "alpha-web",
			Kind:    "production",
			EnvVars: []kube.KusoEnvVar{
				{
					Name: "JWT_SECRET",
					ValueFrom: map[string]any{
						"secretKeyRef": map[string]any{
							"name": "alpha-shared",
							"key":  "JWT_SECRET",
						},
					},
				},
			},
		},
	}

	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", prodEnv),
	)

	body := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"},
			"base": {"ref": "main"},
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}

	envCR, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("preview env not created: %v", err)
	}
	if len(envCR.Spec.EnvVars) != 1 {
		t.Fatalf("preview envVars not inherited: got %d, want 1 (cloned from production)", len(envCR.Spec.EnvVars))
	}
	if envCR.Spec.EnvVars[0].Name != "JWT_SECRET" {
		t.Errorf("preview envVar name: %q, want JWT_SECRET", envCR.Spec.EnvVars[0].Name)
	}
	ref, _ := envCR.Spec.EnvVars[0].ValueFrom["secretKeyRef"].(map[string]any)
	if ref == nil || ref["name"] != "alpha-shared" {
		t.Errorf("preview envVar valueFrom not preserved: %+v", envCR.Spec.EnvVars[0].ValueFrom)
	}
}

// TestDispatch_PROpened_ClonesPerEnvSecretWithRewrittenURLs locks
// in the v0.17.4 fix. Production carries NEXT_PUBLIC_API_URL in a
// per-env Secret tickero-frontend-production-secrets. Without
// cloning + rewriting, preview pods inherit the production URL,
// CSP blocks fetches to api-pr-N, page renders empty.
//
// This test seeds a production env that references a per-env
// Secret holding NEXT_PUBLIC_API_URL=https://api.alpha.example.com,
// then fires a PR-open and asserts the per-PR Secret
// alpha-web-pr-42-secrets exists with the URL rewritten to
// https://api-pr-42.alpha.example.com.
func TestDispatch_PROpened_ClonesPerEnvSecretWithRewrittenURLs(t *testing.T) {
	t.Parallel()

	// Two services so we have multiple prod hosts to rewrite —
	// "web" + "api". The "api" service exists purely so the
	// hostRewrite map carries an api.alpha.example.com entry to
	// substitute inside web's per-env secret.
	prodEnvWeb := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-production",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "web",
				"kuso.sislelabs.com/env":     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:        "alpha",
			Service:        "alpha-web",
			Kind:           "production",
			EnvFromSecrets: []string{"alpha-web-production-secrets"},
			EnvVars: []kube.KusoEnvVar{
				{
					Name: "NEXT_PUBLIC_API_URL",
					ValueFrom: map[string]any{
						"secretKeyRef": map[string]any{
							"name": "alpha-web-production-secrets",
							"key":  "NEXT_PUBLIC_API_URL",
						},
					},
				},
			},
		},
	}
	prodSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-production-secrets",
			Namespace: "kuso",
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"NEXT_PUBLIC_API_URL":  []byte("https://api.alpha.example.com"),
			"NEXT_PUBLIC_SITE_URL": []byte("https://alpha.example.com"),
		},
	}

	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		// web carries the apex baseDomain as a custom domain — the
		// configured-domain branch in buildPreviewHostRewrite catches
		// the apex naturally; no service-name guessing.
		seedSvcWithDomains("alpha", "web", []kube.KusoDomain{{Host: "alpha.example.com", TLS: true}}),
		seedSvc("alpha", "api"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", prodEnvWeb),
	)
	// Seed the source Secret into the typed Clientset.
	if _, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), prodSecret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed prod secret: %v", err)
	}

	body := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {
			"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"},
			"base": {"ref": "main"},
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch pr: %v", err)
	}

	// Per-PR Secret should now exist with URLs rewritten to the
	// preview hosts.
	prSec, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-web-pr-42-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pr secret not created: %v", err)
	}
	gotAPI := string(prSec.Data["NEXT_PUBLIC_API_URL"])
	if gotAPI != "https://api-pr-42.alpha.example.com" {
		t.Errorf("NEXT_PUBLIC_API_URL not rewritten: got %q, want https://api-pr-42.alpha.example.com", gotAPI)
	}
	gotSite := string(prSec.Data["NEXT_PUBLIC_SITE_URL"])
	if gotSite != "https://web-pr-42.alpha.example.com" {
		t.Errorf("NEXT_PUBLIC_SITE_URL not rewritten: got %q, want https://web-pr-42.alpha.example.com", gotSite)
	}

	// The preview env CR's envFromSecrets should reference the new
	// per-PR Secret, not the production one.
	envCR, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
	if err != nil {
		t.Fatalf("preview env not created: %v", err)
	}
	sawPR, sawProd := false, false
	for _, s := range envCR.Spec.EnvFromSecrets {
		if s == "alpha-web-pr-42-secrets" {
			sawPR = true
		}
		if s == "alpha-web-production-secrets" {
			sawProd = true
		}
	}
	if !sawPR {
		t.Errorf("envFromSecrets missing alpha-web-pr-42-secrets: %v", envCR.Spec.EnvFromSecrets)
	}
	if sawProd {
		t.Errorf("envFromSecrets still references production-scoped secret: %v", envCR.Spec.EnvFromSecrets)
	}

	// envVars[].valueFrom.secretKeyRef.name should point at the
	// per-PR Secret, not the production one.
	for _, e := range envCR.Spec.EnvVars {
		if e.Name != "NEXT_PUBLIC_API_URL" || e.ValueFrom == nil {
			continue
		}
		ref, _ := e.ValueFrom["secretKeyRef"].(map[string]any)
		if ref == nil {
			t.Errorf("NEXT_PUBLIC_API_URL valueFrom shape: %+v", e.ValueFrom)
			continue
		}
		if ref["name"] != "alpha-web-pr-42-secrets" {
			t.Errorf("NEXT_PUBLIC_API_URL secretKeyRef.name = %v, want alpha-web-pr-42-secrets", ref["name"])
		}
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

// TestSwapPGCloneSecretRefsInEnvVars is the regression guard for the
// preview DATABASE_READ_URL bug: an envVar whose secretKeyRef points at
// a source addon-conn Secret must be repointed to the per-PR clone, so
// a preview reads from its own DB, not prod. Non-matching refs + literal
// envVars are untouched, and the input slice is not mutated.
func TestSwapPGCloneSecretRefsInEnvVars(t *testing.T) {
	cloneByOrigin := map[string]string{"tickero-db-conn": "tickero-db-pr-36-conn"}

	vars := []kube.KusoEnvVar{
		{Name: "DATABASE_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-db-pr-36-conn", "key": "DATABASE_URL"}}}, // already clone
		{Name: "DATABASE_READ_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-db-conn", "key": "DATABASE_URL"}}}, // → must swap
		{Name: "OTHER", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-api-secrets", "key": "X"}}}, // unrelated
		{Name: "PLAIN", Value: "literal"}, // no valueFrom
	}

	out := swapPGCloneSecretRefsInEnvVars(vars, cloneByOrigin)

	readRef := out[1].ValueFrom["secretKeyRef"].(map[string]any)
	if readRef["name"] != "tickero-db-pr-36-conn" {
		t.Errorf("DATABASE_READ_URL not repointed: %v", readRef["name"])
	}
	if readRef["key"] != "DATABASE_URL" {
		t.Errorf("DATABASE_READ_URL lost its key: %v", readRef["key"])
	}
	if out[2].ValueFrom["secretKeyRef"].(map[string]any)["name"] != "tickero-api-secrets" {
		t.Error("unrelated secretKeyRef should be untouched")
	}
	if out[3].Value != "literal" {
		t.Error("literal envVar should be untouched")
	}
	// Input not mutated (deep-copy on swap).
	if vars[1].ValueFrom["secretKeyRef"].(map[string]any)["name"] != "tickero-db-conn" {
		t.Error("input slice was mutated — must deep-copy")
	}

	// Empty map → identity (returns input unchanged).
	if got := swapPGCloneSecretRefsInEnvVars(vars, nil); len(got) != len(vars) {
		t.Error("nil map should return input")
	}
}

// TestPreviewRewriteEntries_InClusterDNS is the regression test for the
// SSR-reads-production bug: a preview frontend's API_URL points at the
// in-cluster production API (http://tickero-api-production.kuso.svc.cluster.local),
// and the per-PR rewrite must redirect that to the preview's OWN in-cluster
// API service — otherwise the preview SSRs production data (e.g. null prices)
// while only the browser-side NEXT_PUBLIC_API_URL is rewritten.
func TestPreviewRewriteEntries_InClusterDNS(t *testing.T) {
	t.Parallel()
	got := previewRewriteEntries("tickero", "api", 36, "kuso", "tickero.bg", []string{"api.tickero.bg"})

	// Public host (existing behaviour) preserved.
	if got["api.tickero.bg"] != "api-pr-36.tickero.bg" {
		t.Errorf("public host rewrite missing/wrong: %q", got["api.tickero.bg"])
	}
	// In-cluster production service DNS → preview service DNS (the fix).
	wantInCluster := map[string]string{
		"tickero-api-production.kuso.svc.cluster.local": "tickero-api-pr-36.kuso.svc.cluster.local",
		"tickero-api-staging.kuso.svc.cluster.local":    "tickero-api-pr-36.kuso.svc.cluster.local",
		"tickero-api-production.kuso.svc":               "tickero-api-pr-36.kuso.svc",
		"tickero-api-production":                        "tickero-api-pr-36",
	}
	for from, want := range wantInCluster {
		if got[from] != want {
			t.Errorf("in-cluster rewrite %q = %q, want %q", from, got[from], want)
		}
	}
}

// --- SEC-1: fork-PR preview gate ---------------------------------------

func TestIsForkPR(t *testing.T) {
	t.Parallel()
	mk := func(head, base, repo string) *prEvent {
		p := &prEvent{}
		p.PullRequest.Head.Repo.FullName = head
		p.PullRequest.Base.Repo.FullName = base
		p.Repository.FullName = repo
		return p
	}
	cases := []struct {
		name             string
		head, base, repo string
		wantFork         bool
	}{
		{"same repo", "example/alpha", "example/alpha", "example/alpha", false},
		{"same repo case-insensitive", "Example/Alpha", "example/alpha", "example/alpha", false},
		{"fork", "attacker/alpha", "example/alpha", "example/alpha", true},
		{"head absent → no fork signal", "", "example/alpha", "example/alpha", false},
		{"base absent falls back to repo", "example/alpha", "", "example/alpha", false},
		{"base absent fork", "attacker/alpha", "", "example/alpha", true},
	}
	for _, c := range cases {
		if got := isForkPR(mk(c.head, c.base, c.repo)); got != c.wantFork {
			t.Errorf("%s: isForkPR=%v want %v", c.name, got, c.wantFork)
		}
	}
}

func TestAuthorIsTrusted(t *testing.T) {
	t.Parallel()
	trusted := []string{"OWNER", "MEMBER", "COLLABORATOR", "member"}
	untrusted := []string{"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "NONE", ""}
	for _, a := range trusted {
		p := &prEvent{}
		p.PullRequest.AuthorAssociation = a
		if !authorIsTrusted(p) {
			t.Errorf("association %q should be trusted", a)
		}
	}
	for _, a := range untrusted {
		p := &prEvent{}
		p.PullRequest.AuthorAssociation = a
		if authorIsTrusted(p) {
			t.Errorf("association %q should NOT be trusted", a)
		}
	}
}

// TestDispatch_PRFork_UntrustedAuthorSkipped is the SEC-1 regression:
// a PR opened from a fork by an outsider must NOT spin up a preview env
// (which would run their code with production secrets mounted).
func TestDispatch_PRFork_UntrustedAuthorSkipped(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 66,
		"pull_request": {
			"head": {"ref": "evil", "sha": "abcdef0123456789abcdef0123456789abcdef01", "repo": {"full_name": "attacker/alpha"}},
			"base": {"ref": "main", "repo": {"full_name": "example/alpha"}},
			"author_association": "NONE",
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-66"); err == nil {
		t.Error("preview env created for untrusted fork PR — fork-PR RCE gate failed")
	}
	if bs, _ := d.Builds.List(context.Background(), "alpha", "web"); len(bs) != 0 {
		t.Errorf("build triggered for untrusted fork PR: %d builds", len(bs))
	}
}

// TestDispatch_PRFork_TrustedAuthorAllowed: a fork PR by a COLLABORATOR
// (write-level trust) still previews.
func TestDispatch_PRFork_TrustedAuthorAllowed(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 67,
		"pull_request": {
			"head": {"ref": "feat", "sha": "abcdef0123456789abcdef0123456789abcdef01", "repo": {"full_name": "collab/alpha"}},
			"base": {"ref": "main", "repo": {"full_name": "example/alpha"}},
			"author_association": "COLLABORATOR",
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-67"); err != nil {
		t.Errorf("preview env should exist for trusted-author fork PR: %v", err)
	}
}

// TestDispatch_PRFork_OptInAllowsUntrusted: with previews.allowForkPreviews
// the project accepts untrusted fork PRs (private/trusted-repo opt-in).
func TestDispatch_PRFork_OptInAllowsUntrusted(t *testing.T) {
	t.Parallel()
	proj := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/example/alpha", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
			Previews:    &kube.KusoPreviewsSpec{Enabled: true, TTLDays: 5, AllowForkPreviews: true},
		},
	}
	d := newDispatcher(t,
		typedSeed(kube.GVRProjects, "KusoProject", proj),
		seedSvc("alpha", "web"),
	)
	body := []byte(`{
		"action": "opened",
		"number": 68,
		"pull_request": {
			"head": {"ref": "x", "sha": "abcdef0123456789abcdef0123456789abcdef01", "repo": {"full_name": "attacker/alpha"}},
			"base": {"ref": "main", "repo": {"full_name": "example/alpha"}},
			"author_association": "NONE",
			"state": "open"
		},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-68"); err != nil {
		t.Errorf("preview env should exist when allowForkPreviews=true: %v", err)
	}
}
