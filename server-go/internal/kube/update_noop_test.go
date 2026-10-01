package kube

import (
	"context"
	"testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// updateWithRetry used to PUT even when mutate changed nothing, costing an
// apiserver write per env on every addon event.
func TestUpdateWithRetry_SkipsWriteWhenUnchanged(t *testing.T) {
	t.Parallel()
	c := fakeClient(t, seed(GVREnvironments, "KusoEnvironment", "kuso", "p-api-production",
		map[string]any{"project": "p", "service": "p-api"}))
	dyn := c.Dynamic.(*dynamicfake.FakeDynamicClient)
	countUpdates := func() int {
		n := 0
		for _, a := range dyn.Actions() {
			if a.GetVerb() == "update" {
				n++
			}
		}
		return n
	}

	if _, err := c.UpdateKusoEnvironmentWithRetry(context.Background(), "kuso", "p-api-production",
		func(*KusoEnvironment) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if n := countUpdates(); n != 0 {
		t.Fatalf("no-op mutate issued %d update(s), want 0", n)
	}

	out, err := c.UpdateKusoEnvironmentWithRetry(context.Background(), "kuso", "p-api-production",
		func(e *KusoEnvironment) error { e.Spec.Branch = "dev"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n := countUpdates(); n != 1 || out.Spec.Branch != "dev" {
		t.Fatalf("real change: updates=%d branch=%q, want 1 and dev", n, out.Spec.Branch)
	}
}
