package github

import (
	"context"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
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

// newDispatcher seeds the objects plus, for every production service seeded
// without one, a plain staging env (previews need a staging base). Use
// newDispatcherNoStaging to test the gate itself.
func newDispatcher(t *testing.T, seeds ...seed) *Dispatcher {
	t.Helper()
	have := map[string]bool{}
	for _, s := range seeds {
		if s.gvr == kube.GVREnvironments {
			have[s.obj.GetName()] = true
		}
	}
	var staging []seed
	for _, s := range seeds {
		if env := s.obj.GetLabels()[kube.LabelEnv]; s.gvr != kube.GVRServices || (env != "" && env != "production") {
			continue
		}
		labels := s.obj.GetLabels()
		project, short := labels[kube.LabelProject], labels[kube.LabelService]
		if project == "" || short == "" || have[s.obj.GetName()+"-staging"] {
			continue
		}
		staging = append(staging, seedPersistentEnv(project, short, "staging", "staging"))
	}
	return newDispatcherNoStaging(t, append(seeds, staging...)...)
}

func newDispatcherNoStaging(t *testing.T, seeds ...seed) *Dispatcher {
	t.Helper()
	cs := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRAddons:       "KusoAddonList",
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     "pr-" + strconv.Itoa(prNumber),
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project, Service: project + "-" + service, Kind: "preview", Branch: branch,
			// A preview env carries a pullRequest ref — this is what
			// envBranchesByService keys on to EXCLUDE it from branch tracking.
			PullRequest: &kube.KusoPullRequest{Number: prNumber},
		},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

// seedPersistentEnv seeds a long-lived (non-preview) env that tracks a
// branch — e.g. a `staging` env with spec.branch=staging. No pullRequest,
// so envBranchesByService includes it in the branch-tracking set. Carries
// the project/service labels real envs get (envBranchesByService selects on
// LabelProject).
func seedPersistentEnv(project, service, envName, branch string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-" + envName,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     envName,
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Branch: branch},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func TestIsZeroSHA(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"0000000000000000000000000000000000000000": true,  // SHA-1 delete marker
		strings.Repeat("0", 64):                    true,  // SHA-256 delete marker
		"1111111111111111111111111111111111111111": false, // real SHA
		"abc1234": false, // short SHA
		"":        false, // empty
		"000000":  false, // wrong length
	}
	for in, want := range cases {
		if got := isZeroSHA(in); got != want {
			t.Errorf("isZeroSHA(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestDispatch_PushRouting: which services a push builds. want maps
// "project/service" to the expected build count; wantBranch, when set, is
// the branch every build of the first want entry must carry.
func TestDispatch_PushRouting(t *testing.T) {
	t.Parallel()
	alpha := func(extra ...seed) []seed {
		return append([]seed{
			seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
			seedSvc("alpha", "web"),
		}, extra...)
	}
	type svcCount struct {
		project, service string
		n                int
	}
	cases := []struct {
		name       string
		seeds      []seed
		ref, repo  string
		want       []svcCount
		wantBranch string
	}{
		{
			// A staging env tracks the `staging` branch; a push to staging must
			// build the service (so promoteImage can route the image onto that env).
			name:  "OnBranchTrackedByEnv_TriggersBuild",
			seeds: alpha(seedPersistentEnv("alpha", "web", "staging", "staging")),
			ref:   "staging", repo: "example/alpha",
			want:       []svcCount{{"alpha", "web", 1}},
			wantBranch: "staging", // drives per-env promotion
		},
		{
			// A PREVIEW env's branch must NOT widen the push gate: its branch is a
			// PR head ref driven by onPullRequest, not a plain branch to build on
			// push. Only persistent envs count.
			name:  "OnBranchOnlyTrackedByPreviewEnv_Skips",
			seeds: alpha(seedPreviewEnv("alpha", "web", 7, "feature-x")),
			ref:   "feature-x", repo: "example/alpha",
			want: []svcCount{{"alpha", "web", 0}},
		},
		{
			name:  "OnDefaultBranchTriggersBuild",
			seeds: alpha(), ref: "main", repo: "example/alpha",
			want: []svcCount{{"alpha", "web", 1}},
		},
		{
			name:  "OnNonDefaultBranchSkips",
			seeds: alpha(), ref: "dev", repo: "example/alpha",
			want: []svcCount{{"alpha", "web", 0}},
		},
		{
			name:  "UnknownRepoSkips",
			seeds: alpha(), ref: "main", repo: "other/repo",
			want: []svcCount{{"alpha", "web", 0}},
		},
		{
			// A project with two services on two different repos: a push to repo A
			// must build ONLY service A, not service B.
			name: "MultiRepo_BuildsOnlyMatchingService",
			seeds: []seed{
				// Project default repo = the "api" repo; "web" overrides to its own.
				seedProj("alpha", "https://github.com/example/api", "main", false, 0),
				seedSvcWithRepo("alpha", "web", "https://github.com/example/web", nil),
				seedSvc("alpha", "api"), // inherits example/api
			},
			ref: "main", repo: "example/web",
			want: []svcCount{{"alpha", "web", 1}, {"alpha", "api", 0}},
		},
		{
			// A service whose repo lives only on the service (project defaultRepo
			// empty) must still build on a push to that repo. This is the nev-abrom
			// shape — pre-fix it matched nothing because routing only looked at the
			// project default.
			name: "ServiceRepoNoProjectDefault_Builds",
			seeds: []seed{
				seedProj("nevabrom", "", "", false, 0), // no project default repo
				seedSvcWithRepo("nevabrom", "site", "https://github.com/biznesguys/nev-abrom", nil),
			},
			ref: "main", repo: "biznesguys/nev-abrom",
			want: []svcCount{{"nevabrom", "site", 1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDispatcher(t, tc.seeds...)
			body := []byte(`{
				"ref": "refs/heads/` + tc.ref + `",
				"repository": {"full_name": "` + tc.repo + `", "default_branch": "main"}
			}`)
			if err := d.Dispatch(context.Background(), "push", body); err != nil {
				t.Fatalf("Dispatch push: %v", err)
			}
			for i, w := range tc.want {
				bs, err := d.Builds.List(context.Background(), w.project, w.service)
				if err != nil {
					t.Fatalf("List builds: %v", err)
				}
				if len(bs) != w.n {
					t.Fatalf("%s/%s: got %d builds, want %d", w.project, w.service, len(bs), w.n)
				}
				if i == 0 && tc.wantBranch != "" && bs[0].Spec.Branch != tc.wantBranch {
					t.Errorf("build branch = %q, want %q", bs[0].Spec.Branch, tc.wantBranch)
				}
			}
		})
	}
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

// --- multi-repo project routing (per-service repo matching) ----------

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

	stagingEnvWeb := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-staging",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "web",
				"kuso.sislelabs.com/env":     "staging",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha",
			Service: "alpha-web",
			Kind:    "custom",
			// A plain LITERAL envVar (not a secretKeyRef) — this is the
			// path rewriteEnvVarValues touches and the one Bug 5 hits.
			// It names staging's api host, which must map to the preview.
			EnvVars: []kube.KusoEnvVar{
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.alpha.example.com"},
			},
		},
	}
	apiStaging := seedPersistentEnv("alpha", "api", "staging", "staging")
	_ = unstructured.SetNestedField(apiStaging.obj.Object, "api-staging.alpha.example.com", "spec", "host")

	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		// "api" service so the hostRewrite map carries an
		// api-staging.alpha.example.com → api-pr-42 entry.
		seedSvc("alpha", "web"),
		seedSvc("alpha", "api"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", stagingEnvWeb),
		apiStaging,
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
// image, as left behind after a first PR open. previewEnv "" seeds an
// unmarked (push) build.
func seedSucceededBuild(project, service, sha, repo, tag, previewEnv string) seed {
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
	if previewEnv != "" {
		b.Annotations[builds.AnnPreviewEnv] = previewEnv
	}
	return typedSeed(kube.GVRBuilds, "KusoBuild", b)
}

// TestDispatch_PRPreview_IgnoresPushBuildOfSameSHA is the BUILD-01
// regression: a PR whose head branch is tracked by a persistent env shares
// its commit with that env's push build. The push build (unmarked) must
// neither be stamped onto the preview (it baked the persistent env's
// build-time vars) nor, while running, suppress the preview's own build
// (it never promotes into a preview env).
func TestDispatch_PRPreview_IgnoresPushBuildOfSameSHA(t *testing.T) {
	t.Parallel()
	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	pushBuild := func(done bool) seed {
		s := seedSucceededBuild("alpha", "web", sha, "registry/alpha/web", sha[:12], "")
		if !done {
			unstructured.RemoveNestedField(s.obj.Object, "spec", "done")
			unstructured.RemoveNestedField(s.obj.Object, "spec", "image")
			unstructured.RemoveNestedField(s.obj.Object, "metadata", "annotations")
		}
		_ = unstructured.SetNestedField(s.obj.Object, "staging", "spec", "branch")
		return s
	}
	for name, done := range map[string]bool{"succeeded": true, "in-flight": false} {
		t.Run(name, func(t *testing.T) {
			d := newDispatcher(t,
				seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
				seedSvc("alpha", "web"),
				pushBuild(done),
			)
			body := []byte(`{
				"action": "opened",
				"number": 42,
				"pull_request": {"head": {"ref": "staging", "sha": "` + sha + `"}, "state": "open"},
				"repository": {"full_name": "example/alpha"}
			}`)
			if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-42")
			if err != nil {
				t.Fatalf("get env: %v", err)
			}
			if env.Spec.Image != nil {
				t.Errorf("push build's image stamped onto preview: %+v", env.Spec.Image)
			}
			bs, _ := d.Kube.ListKusoBuildsByLabels(context.Background(), "kuso", map[string]string{kube.LabelProject: "alpha"})
			var preview int
			for _, b := range bs {
				if b.Annotations[builds.AnnPreviewEnv] == "alpha-web-pr-42" {
					preview++
				}
			}
			if preview != 1 {
				t.Errorf("want 1 preview build alongside the push build, got %d (of %d)", preview, len(bs))
			}
		})
	}
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
		seedSucceededBuild("alpha", "web", sha, "registry/alpha/web", sha[:12], "alpha-web-pr-42"),
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
	cases := []struct {
		name    string
		svc     seed
		number  string
		envName string
		wantCmd []string // nil: the preview must carry no release
	}{
		{name: "PropagatesReleaseHook", svc: seedSvcWithRelease("alpha", "api", cmd),
			number: "51", envName: "alpha-api-pr-51", wantCmd: cmd},
		// A service without a release hook yields a preview with no release
		// (no-op, unchanged behaviour — most services don't migrate).
		{name: "NoReleaseHook_LeavesReleaseNil", svc: seedSvc("alpha", "web"),
			number: "52", envName: "alpha-web-pr-52"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDispatcher(t,
				seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
				tc.svc,
			)
			sha := strings.Repeat(tc.number, 20)
			body := []byte(`{
				"action": "opened",
				"number": ` + tc.number + `,
				"pull_request": {"head": {"ref": "feat/x", "sha": "` + sha + `"}, "state": "open"},
				"repository": {"full_name": "example/alpha"}
			}`)
			if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
				t.Fatalf("Dispatch pr: %v", err)
			}
			env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", tc.envName)
			if err != nil {
				t.Fatalf("get env: %v", err)
			}
			if tc.wantCmd == nil {
				if env.Spec.Release != nil {
					t.Errorf("preview env should have nil release for a service without a release hook, got %+v", env.Spec.Release)
				}
				return
			}
			if env.Spec.Release == nil {
				t.Fatalf("preview env should carry the parent service's release hook, got nil")
			}
			if got := env.Spec.Release.Command; !reflect.DeepEqual(got, tc.wantCmd) {
				t.Errorf("release command: got %v, want %v", got, tc.wantCmd)
			}
		})
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

// TestDispatch_BranchDelete_CancelsInFlightBuilds locks in the phantom-
// alert fix: a push that DELETES a branch (deleted=true, all-zeros after
// SHA) must cancel any queued/running builds for that branch instead of
// leaving them to clone a vanished ref and page @here.
func TestDispatch_BranchDelete_CancelsInFlightBuilds(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 0),
		seedSvc("alpha", "web"),
		seedPersistentEnv("alpha", "web", "feature", "feature-x"),
	)
	ctx := context.Background()
	// A push to feature-x triggers a build (a persistent env tracks it).
	push := []byte(`{
		"ref": "refs/heads/feature-x",
		"after": "1111111111111111111111111111111111111111",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}
	}`)
	if err := d.Dispatch(ctx, "push", push); err != nil {
		t.Fatalf("Dispatch push: %v", err)
	}
	bs, _ := d.Builds.List(ctx, "alpha", "web")
	if len(bs) != 1 {
		t.Fatalf("expected 1 in-flight build before delete, got %d", len(bs))
	}
	// Now delete the branch: deleted=true, all-zeros after SHA.
	del := []byte(`{
		"ref": "refs/heads/feature-x",
		"deleted": true,
		"after": "0000000000000000000000000000000000000000",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}
	}`)
	if err := d.Dispatch(ctx, "push", del); err != nil {
		t.Fatalf("Dispatch delete push: %v", err)
	}
	// The build must now be cancelled, not still in-flight, and NOT a
	// second build (the delete push must not trigger a new build).
	bs, _ = d.Builds.List(ctx, "alpha", "web")
	if len(bs) != 1 {
		t.Fatalf("expected exactly 1 build after delete (no new build), got %d", len(bs))
	}
	if got := bs[0].Annotations["kuso.sislelabs.com/build-phase"]; got != "cancelled" {
		t.Errorf("build phase = %q, want cancelled", got)
	}
}

// TestDispatch_PRClosed_CancelsInFlightBuilds is the PR-close counterpart:
// closing/merging a PR must cancel the preview build for its head ref.
func TestDispatch_PRClosed_CancelsInFlightBuilds(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	ctx := context.Background()
	// Open the PR → creates the preview env + an in-flight build on the
	// head ref feat/x.
	open := []byte(`{
		"action": "opened",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abc1234"}, "base": {"ref": "main"}, "state": "open", "author_association": "OWNER"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(ctx, "pull_request", open); err != nil {
		t.Fatalf("Dispatch pr opened: %v", err)
	}
	bs, _ := d.Builds.List(ctx, "alpha", "web")
	if len(bs) != 1 {
		t.Fatalf("expected 1 preview build after PR open, got %d", len(bs))
	}
	// Close the PR → the head-ref build must be cancelled.
	closed := []byte(`{
		"action": "closed",
		"number": 42,
		"pull_request": {"head": {"ref": "feat/x", "sha": "abc1234"}, "base": {"ref": "main"}, "state": "closed"},
		"repository": {"full_name": "example/alpha"}
	}`)
	if err := d.Dispatch(ctx, "pull_request", closed); err != nil {
		t.Fatalf("Dispatch pr closed: %v", err)
	}
	bs, _ = d.Builds.List(ctx, "alpha", "web")
	if len(bs) != 1 {
		t.Fatalf("expected 1 build after PR close, got %d", len(bs))
	}
	if got := bs[0].Annotations["kuso.sislelabs.com/build-phase"]; got != "cancelled" {
		t.Errorf("build phase = %q, want cancelled", got)
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
	if err := d.Dispatch(context.Background(), "push", []byte(`{not-json`)); err == nil {
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

func TestDispatch_PRFork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		project     seed
		number      string
		headRepo    string
		association string
		wantPreview bool
	}{
		{
			// SEC-1 regression: a PR opened from a fork by an outsider must NOT
			// spin up a preview env (which would run their code with production
			// secrets mounted).
			name:    "UntrustedAuthorSkipped",
			project: seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
			number:  "66", headRepo: "attacker/alpha", association: "NONE",
		},
		{
			// A fork PR by a COLLABORATOR (write-level trust) still previews.
			name:    "TrustedAuthorAllowed",
			project: seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
			number:  "67", headRepo: "collab/alpha", association: "COLLABORATOR",
			wantPreview: true,
		},
		{
			// With previews.allowForkPreviews the project accepts untrusted fork
			// PRs (private/trusted-repo opt-in).
			name: "OptInAllowsUntrusted",
			project: typedSeed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
				Spec: kube.KusoProjectSpec{
					DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/example/alpha", DefaultBranch: "main"},
					BaseDomain:  "alpha.example.com",
					Previews:    &kube.KusoPreviewsSpec{Enabled: true, TTLDays: 5, AllowForkPreviews: true},
				},
			}),
			number: "68", headRepo: "attacker/alpha", association: "NONE",
			wantPreview: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newDispatcher(t, tc.project, seedSvc("alpha", "web"))
			body := []byte(`{
				"action": "opened",
				"number": ` + tc.number + `,
				"pull_request": {
					"head": {"ref": "feat", "sha": "abcdef0123456789abcdef0123456789abcdef01", "repo": {"full_name": "` + tc.headRepo + `"}},
					"base": {"ref": "main", "repo": {"full_name": "example/alpha"}},
					"author_association": "` + tc.association + `",
					"state": "open"
				},
				"repository": {"full_name": "example/alpha"}
			}`)
			if err := d.Dispatch(context.Background(), "pull_request", body); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			_, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-"+tc.number)
			if tc.wantPreview {
				if err != nil {
					t.Errorf("preview env should exist: %v", err)
				}
				return
			}
			if err == nil {
				t.Error("preview env created for untrusted fork PR — fork-PR RCE gate failed")
			}
			if bs, _ := d.Builds.List(context.Background(), "alpha", "web"); len(bs) != 0 {
				t.Errorf("build triggered for untrusted fork PR: %d builds", len(bs))
			}
		})
	}
}

// TestUnclonedAddonConnRefs is the regression guard for the tickero PR-52
// leak: the project's native "db" addon was replaced by an external "psdb"
// while PR 52 was open, so the clone (tickero-db-pr-52) no longer matched
// any live source. The name-derived swap map missed, the swap silently
// no-op'd, and the preview's DATABASE_URL kept resolving to the PRODUCTION
// pooler — surfacing days later as a crashloop, not as an error.
//
// A ref into a project addon-conn that the swap did NOT redirect must be
// reported so the leak is greppable at the moment it happens.
func TestUnclonedAddonConnRefs(t *testing.T) {
	projectConns := []string{"tickero-psdb-conn", "tickero-cache-conn"}
	// Only the cache got cloned; psdb (external, replaced the old db) did not.
	cloneByOrigin := map[string]string{"tickero-cache-conn": "tickero-cache-pr-52-conn"}

	vars := []kube.KusoEnvVar{
		{Name: "DATABASE_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-psdb-conn", "key": "POOLER_URL"}}}, // leak
		{Name: "DATABASE_READ_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-psdb-conn", "key": "POOLER_URL"}}}, // leak
		{Name: "REDIS_URL", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-cache-conn", "key": "URL"}}}, // swapped, fine
		{Name: "API_KEY", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "tickero-api-secrets", "key": "K"}}}, // not an addon conn
		{Name: "PLAIN", Value: "literal"},
	}

	got := unclonedAddonConnRefs(vars, projectConns, cloneByOrigin)
	want := []string{"DATABASE_URL->tickero-psdb-conn", "DATABASE_READ_URL->tickero-psdb-conn"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}

	// Fully-cloned env → nothing to report.
	full := map[string]string{
		"tickero-psdb-conn":  "tickero-psdb-pr-52-conn",
		"tickero-cache-conn": "tickero-cache-pr-52-conn",
	}
	if leaked := unclonedAddonConnRefs(vars, projectConns, full); len(leaked) != 0 {
		t.Errorf("fully-cloned env should report nothing, got %v", leaked)
	}
}

// TestSwapPGCloneSecrets_UsesAuthoritativeMap pins the envFromSecrets swap
// to the cloner-supplied map. The old implementation re-derived the origin
// by stripping "-pr-<N>-conn" off the clone name, which produced the WRONG
// origin ("tickero-db-conn") for a project whose source addon had been
// renamed to psdb — and failed open, leaving production mounted.
func TestSwapPGCloneSecrets_UsesAuthoritativeMap(t *testing.T) {
	source := []string{"tickero-psdb-conn", "tickero-cache-conn", "tickero-api-secrets"}
	// The clone is named after the OLD source ("db"), but the cloner reports
	// the true pairing against the CURRENT source name.
	m := map[string]string{"tickero-psdb-conn": "tickero-db-pr-52-conn"}

	out := swapPGCloneSecrets(source, m)
	if out[0] != "tickero-db-pr-52-conn" {
		t.Errorf("psdb-conn not swapped to the clone: %q", out[0])
	}
	if out[1] != "tickero-cache-conn" || out[2] != "tickero-api-secrets" {
		t.Errorf("non-cloned secrets must pass through verbatim: %v", out)
	}
	if got := swapPGCloneSecrets(source, nil); got[0] != "tickero-psdb-conn" {
		t.Error("empty map should return the source list unchanged")
	}
}
