package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A downgraded server (or the auto-revert after a failed leader gate)
// must not delete spec fields a newer release wrote that this binary has
// no struct field for.
func TestUpdate_PreservesUnknownSpecFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := fakeClient(t, seed(GVREnvironments, "KusoEnvironment", "kuso", "p-api-production",
		map[string]any{
			"project":     "p",
			"service":     "p-api",
			"futureField": map[string]any{"enabled": true},
			"placement":   map[string]any{"nodes": []any{"n1"}, "futureKey": "keep"},
		}))

	check := func(step string) {
		t.Helper()
		raw, err := c.Dynamic.Resource(GVREnvironments).Namespace("kuso").Get(ctx, "p-api-production", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _ := unstructured.NestedBool(raw.Object, "spec", "futureField", "enabled"); !v {
			t.Fatalf("%s: spec.futureField dropped: %v", step, raw.Object["spec"])
		}
		if v, _, _ := unstructured.NestedString(raw.Object, "spec", "placement", "futureKey"); v != "keep" {
			t.Fatalf("%s: spec.placement.futureKey dropped: %v", step, raw.Object["spec"])
		}
	}

	if _, err := c.UpdateKusoEnvironmentWithRetry(ctx, "kuso", "p-api-production",
		func(e *KusoEnvironment) error { e.Spec.Branch = "dev"; return nil }); err != nil {
		t.Fatal(err)
	}
	check("updateWithRetry")

	e, err := c.GetKusoEnvironment(ctx, "kuso", "p-api-production")
	if err != nil {
		t.Fatal(err)
	}
	e.Spec.Branch = "main"
	e.Spec.Placement = nil // a known field the caller clears must stay cleared
	out, err := c.UpdateKusoEnvironment(ctx, "kuso", e)
	if err != nil {
		t.Fatal(err)
	}
	if out.Spec.Branch != "main" || out.Spec.Placement != nil {
		t.Fatalf("typed update lost the caller's change: branch=%q placement=%v", out.Spec.Branch, out.Spec.Placement)
	}
	raw, _ := c.Dynamic.Resource(GVREnvironments).Namespace("kuso").Get(ctx, "p-api-production", metav1.GetOptions{})
	if v, _, _ := unstructured.NestedBool(raw.Object, "spec", "futureField", "enabled"); !v {
		t.Fatalf("typed update dropped spec.futureField: %v", raw.Object["spec"])
	}
}
