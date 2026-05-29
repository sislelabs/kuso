package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// TestSubscriptionFields_EmptySliceRoundTrips guards the omitempty fix:
// SharedEnvKeys / SubscribedAddons distinguish nil ("legacy: mount all")
// from an empty []string{} ("subscribe to nothing"). If these fields carry
// json:"...,omitempty", runtime.DefaultUnstructuredConverter drops a non-nil
// empty slice exactly like nil — so persisting an empty subscription (e.g.
// a public frontend that must NOT hold DATABASE_URL or JWT_SECRET) silently
// reverts to mount-all. This test asserts the empty slice survives the
// unstructured round-trip that every CR write/read goes through.
func TestSubscriptionFields_EmptySliceRoundTrips(t *testing.T) {
	t.Parallel()

	in := &KusoService{
		Spec: KusoServiceSpec{
			Project:          "alpha",
			SharedEnvKeys:    []string{}, // subscribe to NOTHING
			SubscribedAddons: []string{}, // subscribe to NO addons
		},
	}

	// ToUnstructured → FromUnstructured is exactly what create/update +
	// read do in crds.go.
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(in)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}

	var out KusoService
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &out); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}

	if out.Spec.SharedEnvKeys == nil {
		t.Error("SharedEnvKeys round-tripped empty []string{} → nil: omitempty collapsed the empty subscription back to legacy mount-all")
	}
	if len(out.Spec.SharedEnvKeys) != 0 {
		t.Errorf("SharedEnvKeys: want empty non-nil, got %v", out.Spec.SharedEnvKeys)
	}
	if out.Spec.SubscribedAddons == nil {
		t.Error("SubscribedAddons round-tripped empty []string{} → nil: omitempty collapsed the empty subscription back to legacy auto-mount-all")
	}
	if len(out.Spec.SubscribedAddons) != 0 {
		t.Errorf("SubscribedAddons: want empty non-nil, got %v", out.Spec.SubscribedAddons)
	}
}

// TestSubscriptionFields_NilStaysNil confirms the OTHER half of the
// invariant survives: a nil subscription (legacy / unset) must remain nil
// so existing pre-subscription services keep mounting everything.
func TestSubscriptionFields_NilStaysNil(t *testing.T) {
	t.Parallel()

	in := &KusoService{Spec: KusoServiceSpec{Project: "alpha"}}

	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(in)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	var out KusoService
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &out); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if out.Spec.SharedEnvKeys != nil {
		t.Errorf("nil SharedEnvKeys should stay nil, got %v", out.Spec.SharedEnvKeys)
	}
	if out.Spec.SubscribedAddons != nil {
		t.Errorf("nil SubscribedAddons should stay nil, got %v", out.Spec.SubscribedAddons)
	}
}
