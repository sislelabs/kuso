package github

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
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
