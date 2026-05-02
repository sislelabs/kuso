package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// seedObj pairs an unstructured CR with the GVR the fake tracker should
// index it under. We seed via tracker.Create rather than the constructor's
// objs varargs because Add() routes through meta.UnsafeGuessKindToResource,
// which mispluralizes Kuso → "kusos" instead of "kusoes".
type seedObj struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

// fakeClient builds a *Client backed by dynamic/fake and seeds it with the
// given objects under their explicit GVRs.
func fakeClient(t *testing.T, seeds ...seedObj) *Client {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		GVRKuso:         "KusoList",
		GVRProjects:     "KusoProjectList",
		GVRServices:     "KusoServiceList",
		GVREnvironments: "KusoEnvironmentList",
		GVRAddons:       "KusoAddonList",
		GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s/%s: %v", s.gvr.Resource, s.obj.GetName(), err)
		}
	}
	return &Client{Dynamic: dyn}
}

// seed builds a seedObj from a GVR + a typed-spec map, with kind derived
// by stripping the trailing "s" or "es" — but explicit kind is required
// because Kuso/kusoes is not a standard plural pair. We pass kind in.
func seed(gvr schema.GroupVersionResource, kind, namespace, name string, spec map[string]any) seedObj {
	return seedObj{gvr: gvr, obj: crd(gvr, kind, namespace, name, spec)}
}

// crd builds an *unstructured.Unstructured CR for seeding the fake client.
// Using unstructured directly (instead of typed structs) keeps the test
// code agnostic of how runtime.DefaultUnstructuredConverter encodes types
// — exactly the same path the real API server returns.
func crd(gvr schema.GroupVersionResource, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvr.Group,
		Version: gvr.Version,
		Kind:    kind,
	})
	u.SetNamespace(namespace)
	u.SetName(name)
	if spec != nil {
		_ = unstructured.SetNestedField(u.Object, spec, "spec")
	}
	return u
}

func TestListKusoEnvironments_DecodesTypedFields(t *testing.T) {
	t.Parallel()
	want := map[string]any{
		"project":          "alpha",
		"service":          "web",
		"kind":             "preview",
		"branch":           "feat/x",
		"host":             "alpha-pr-7.example.com",
		"replicaCount":     int64(2),
		"tlsEnabled":       true,
		"clusterIssuer":    "letsencrypt-prod",
		"ingressClassName": "traefik",
		"secretsRev":       "rev-1",
		"envFromSecrets":   []any{"shared", "scoped-web"},
		"image": map[string]any{
			"repository": "registry.local/alpha-web",
			"tag":        "abc123def456",
			"pullPolicy": "IfNotPresent",
		},
		"pullRequest": map[string]any{
			"number":  int64(7),
			"headRef": "feat/x",
		},
		"autoscaling": map[string]any{
			"enabled":                        true,
			"minReplicas":                    int64(2),
			"maxReplicas":                    int64(5),
			"targetCPUUtilizationPercentage": int64(70),
		},
	}
	c := fakeClient(t, seed(GVREnvironments, "KusoEnvironment", "kuso", "alpha-web-pr7", want))

	got, err := c.ListKusoEnvironments(context.Background(), "kuso")
	if err != nil {
		t.Fatalf("ListKusoEnvironments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 env, got %d", len(got))
	}
	env := got[0]

	if env.Name != "alpha-web-pr7" {
		t.Errorf("Name: got %q, want alpha-web-pr7", env.Name)
	}
	if env.Spec.Project != "alpha" || env.Spec.Service != "web" {
		t.Errorf("project/service: got %q/%q", env.Spec.Project, env.Spec.Service)
	}
	if env.Spec.Kind != "preview" || env.Spec.Branch != "feat/x" {
		t.Errorf("kind/branch: got %q/%q", env.Spec.Kind, env.Spec.Branch)
	}
	if env.Spec.SecretsRev != "rev-1" {
		t.Errorf("secretsRev: got %q, want rev-1", env.Spec.SecretsRev)
	}
	if env.Spec.ReplicaCount != 2 {
		t.Errorf("replicaCount: got %d, want 2", env.Spec.ReplicaCount)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != "abc123def456" {
		t.Errorf("image.tag: %+v", env.Spec.Image)
	}
	if env.Spec.PullRequest == nil || env.Spec.PullRequest.Number != 7 {
		t.Errorf("pullRequest.number: %+v", env.Spec.PullRequest)
	}
	if env.Spec.Autoscaling == nil || !env.Spec.Autoscaling.Enabled || env.Spec.Autoscaling.MaxReplicas != 5 {
		t.Errorf("autoscaling: %+v", env.Spec.Autoscaling)
	}
	if got, want := env.Spec.EnvFromSecrets, []string{"shared", "scoped-web"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("envFromSecrets: %v", got)
	}
}

func TestGetKusoProject_DecodesGitHubInstallationID(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, seed(GVRProjects, "KusoProject", "kuso", "alpha", map[string]any{
		"description": "alpha service",
		"baseDomain":  "alpha.example.com",
		"defaultRepo": map[string]any{
			"url":           "https://github.com/example/alpha",
			"defaultBranch": "main",
		},
		// installationId is int64-typed in the CRD — the converter must
		// land it in the int64 struct field.
		"github": map[string]any{
			"installationId": int64(987654),
		},
		"previews": map[string]any{
			"enabled": true,
			"ttlDays": int64(7),
		},
	}))

	got, err := c.GetKusoProject(context.Background(), "kuso", "alpha")
	if err != nil {
		t.Fatalf("GetKusoProject: %v", err)
	}
	if got.Spec.GitHub == nil || got.Spec.GitHub.InstallationID != 987654 {
		t.Errorf("github.installationId: %+v", got.Spec.GitHub)
	}
	if got.Spec.Previews == nil || !got.Spec.Previews.Enabled || got.Spec.Previews.TTLDays != 7 {
		t.Errorf("previews: %+v", got.Spec.Previews)
	}
	if got.Spec.DefaultRepo == nil || got.Spec.DefaultRepo.DefaultBranch != "main" {
		t.Errorf("defaultRepo: %+v", got.Spec.DefaultRepo)
	}
}

func TestListKusoBuilds_DecodesPhaseStatusAsFreeform(t *testing.T) {
	t.Parallel()
	s := seed(GVRBuilds, "KusoBuild", "kuso", "alpha-web-abc12345", map[string]any{
		"project": "alpha",
		"service": "web",
		"ref":     "abc12345",
		"branch":  "main",
		"image": map[string]any{
			"repository": "registry.local/alpha-web",
			"tag":        "abc12345",
		},
		"strategy": "dockerfile",
	})
	// status is x-kubernetes-preserve-unknown-fields → we keep it as
	// map[string]any. Verify that round-trips faithfully.
	_ = unstructured.SetNestedField(s.obj.Object, "Succeeded", "status", "phase")
	_ = unstructured.SetNestedField(s.obj.Object, "2026-05-02T10:00:00Z", "status", "completedAt")

	c := fakeClient(t, s)
	got, err := c.ListKusoBuilds(context.Background(), "kuso")
	if err != nil {
		t.Fatalf("ListKusoBuilds: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 build, got %d", len(got))
	}
	b := got[0]
	if b.Spec.Project != "alpha" || b.Spec.Service != "web" || b.Spec.Ref != "abc12345" {
		t.Errorf("spec: %+v", b.Spec)
	}
	if b.Spec.Image == nil || b.Spec.Image.Tag != "abc12345" {
		t.Errorf("image: %+v", b.Spec.Image)
	}
	if got := b.Status["phase"]; got != "Succeeded" {
		t.Errorf("status.phase: got %v, want Succeeded", got)
	}
}

func TestGetKuso_PreservesUnknownSpec(t *testing.T) {
	t.Parallel()
	// The Kuso CR spec is preserve-unknown-fields, so the operator and
	// the seed flow stuff arbitrary keys under it. The wrapper must keep
	// every key — the config package decodes from there.
	c := fakeClient(t, seed(GVRKuso, "Kuso", "kuso", "kuso", map[string]any{
		"kuso": map[string]any{
			"adminDisabled": false,
			"templates": map[string]any{
				"enabled": true,
			},
		},
		"runpacks": []any{
			map[string]any{"name": "go", "language": "go"},
		},
	}))

	got, err := c.GetKuso(context.Background(), "kuso", "kuso")
	if err != nil {
		t.Fatalf("GetKuso: %v", err)
	}
	kuso, ok := got.Spec["kuso"].(map[string]any)
	if !ok {
		t.Fatalf("spec.kuso missing or wrong type: %+v", got.Spec)
	}
	templates, ok := kuso["templates"].(map[string]any)
	if !ok || templates["enabled"] != true {
		t.Errorf("spec.kuso.templates: %+v", kuso["templates"])
	}
	runpacks, ok := got.Spec["runpacks"].([]any)
	if !ok || len(runpacks) != 1 {
		t.Fatalf("spec.runpacks: %+v", got.Spec["runpacks"])
	}
}

func TestList_NamespaceScoping(t *testing.T) {
	t.Parallel()
	c := fakeClient(t,
		seed(GVRServices, "KusoService", "kuso", "web", map[string]any{"project": "alpha"}),
		seed(GVRServices, "KusoService", "other", "api", map[string]any{"project": "beta"}),
	)

	gotKuso, err := c.ListKusoServices(context.Background(), "kuso")
	if err != nil {
		t.Fatalf("ListKusoServices kuso: %v", err)
	}
	if len(gotKuso) != 1 || gotKuso[0].Name != "web" {
		t.Errorf("kuso ns: %+v", gotKuso)
	}

	gotOther, err := c.ListKusoServices(context.Background(), "other")
	if err != nil {
		t.Fatalf("ListKusoServices other: %v", err)
	}
	if len(gotOther) != 1 || gotOther[0].Name != "api" {
		t.Errorf("other ns: %+v", gotOther)
	}
}

func TestGetKusoEnvironment_NotFound(t *testing.T) {
	t.Parallel()
	c := fakeClient(t)
	_, err := c.GetKusoEnvironment(context.Background(), "kuso", "missing")
	if err == nil {
		t.Fatalf("expected error for missing env, got nil")
	}
}

// Sanity: verify our GVR constants line up with a fixture object's GVK.
func TestGVRs_MatchKindNamingConvention(t *testing.T) {
	t.Parallel()
	cases := []struct {
		gvr  schema.GroupVersionResource
		kind string
	}{
		{GVRKuso, "Kuso"},
		{GVRProjects, "KusoProject"},
		{GVRServices, "KusoService"},
		{GVREnvironments, "KusoEnvironment"},
		{GVRAddons, "KusoAddon"},
		{GVRBuilds, "KusoBuild"},
	}
	for _, tc := range cases {
		// Seed → list round-trip via the dynamic client. If the GVR/Kind
		// pair is wrong, the fake will silently return zero items, so we
		// assert exactly one comes back.
		c := fakeClient(t, seed(tc.gvr, tc.kind, "kuso", "x", map[string]any{}))
		raw, err := c.Dynamic.Resource(tc.gvr).Namespace("kuso").List(context.Background(), metav1.ListOptions{})
		if err != nil {
			t.Errorf("list %s: %v", tc.gvr.Resource, err)
			continue
		}
		if len(raw.Items) != 1 {
			t.Errorf("%s: got %d items, want 1", tc.gvr.Resource, len(raw.Items))
		}
	}
}
