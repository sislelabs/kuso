package kube

import (
	"context"
	"testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func envWithLabel(scope string) seedObj {
	s := seed(GVREnvironments, "KusoEnvironment", "kuso", "p-web-staging", map[string]any{
		"project": "p", "service": "p-web", "kind": "production",
	})
	s.obj.SetLabels(map[string]string{LabelProject: "p", LabelEnv: scope})
	return s
}

func countUpdates(c *Client) int {
	n := 0
	for _, a := range c.Dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetVerb() == "update" {
			n++
		}
	}
	return n
}

// A real write carries the env label into spec.envScope, which is the only
// way the chart can see it.
func TestUpdateWithRetry_StampsEnvScopeOnRealWrite(t *testing.T) {
	c := fakeClient(t, envWithLabel("staging"))
	got, err := c.UpdateKusoEnvironmentWithRetry(context.Background(), "kuso", "p-web-staging", func(e *KusoEnvironment) error {
		e.Spec.Branch = "develop"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.EnvScope != "staging" {
		t.Fatalf("envScope = %q, want staging", got.Spec.EnvScope)
	}
}

// The stamp alone must not turn a no-op update into a write: the addon
// refresh runs this on every env per event, and a write rolls the pods.
func TestUpdateWithRetry_StampAloneIsNotAWrite(t *testing.T) {
	c := fakeClient(t, envWithLabel("staging"))
	if _, err := c.UpdateKusoEnvironmentWithRetry(context.Background(), "kuso", "p-web-staging", func(*KusoEnvironment) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := countUpdates(c); n != 0 {
		t.Fatalf("no-op mutate issued %d updates, want 0", n)
	}
}

func TestStampKusoEnvironmentScope_PatchesLabelAndSpec(t *testing.T) {
	c := fakeClient(t, envWithLabel(""))
	if err := c.StampKusoEnvironmentScope(context.Background(), "kuso", "p-web-staging", "production"); err != nil {
		t.Fatal(err)
	}
	e, err := c.GetKusoEnvironment(context.Background(), "kuso", "p-web-staging")
	if err != nil {
		t.Fatal(err)
	}
	if e.Labels[LabelEnv] != "production" || e.Spec.EnvScope != "production" {
		t.Fatalf("label=%q envScope=%q", e.Labels[LabelEnv], e.Spec.EnvScope)
	}
}
