package remediate

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/audit"
	"kuso/server/internal/kube"
	"kuso/server/internal/reconcilehealth"
)

// newUnstructuredAddon builds a minimal KusoAddon unstructured object for
// the dynamic fake to patch.
func newUnstructuredAddon(ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     map[string]any{"kind": "postgres"},
	}}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	return u
}

// fixedClock returns a deterministic time so the reconcile annotation is
// predictable in assertions.
func fixedClock() time.Time { return time.Unix(1700000000, 0) }

// capturingAuditor records the entries Apply emits.
type capturingAuditor struct{ entries []audit.Entry }

func (c *capturingAuditor) Log(_ context.Context, e audit.Entry) { c.entries = append(c.entries, e) }

func TestApply_RefusesUnattendedUnsafe(t *testing.T) {
	r := &Remediator{Now: fixedClock}
	iss := reconcilehealth.Issue{
		Resource: "tickero-queue",
		Kind:     reconcilehealth.KindSpecMismatch,
		Action:   reconcilehealth.ActionForceReconcile,
		Safe:     false, // spec-mismatch needs human direction
	}
	// auto=true must refuse an unsafe issue.
	if _, err := r.Apply(context.Background(), iss, "system", true); err == nil {
		t.Fatal("expected unattended remediation of an unsafe issue to be refused")
	}
}

func TestApply_NoActionErrors(t *testing.T) {
	r := &Remediator{Now: fixedClock}
	iss := reconcilehealth.Issue{Resource: "x", Action: reconcilehealth.ActionNone}
	if _, err := r.Apply(context.Background(), iss, "u", false); err == nil {
		t.Fatal("expected an error when the issue has no automated action")
	}
}

func TestApply_OrphanRecreate_DeletesSTSAndReconciles(t *testing.T) {
	const ns, name = "kuso", "scubatony-db"

	// Seed a StatefulSet so the orphan-delete has something to remove.
	cs := kubefake.NewSimpleClientset(&appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	})

	// Dynamic fake for the CR annotation patch. Register the addon GVR.
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRAddons:       "KusoAddonList",
		kube.GVREnvironments: "KusoEnvironmentList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	// Seed the addon CR object so Patch has a target.
	addonObj := newUnstructuredAddon(ns, name)
	if _, err := dyn.Resource(kube.GVRAddons).Namespace(ns).Create(context.Background(), addonObj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed addon CR: %v", err)
	}

	aud := &capturingAuditor{}
	r := &Remediator{
		Kube:  &kube.Client{Clientset: cs, Dynamic: dyn},
		Audit: aud,
		Now:   fixedClock,
	}
	iss := reconcilehealth.Issue{
		Resource:  name,
		Namespace: ns,
		Project:   "scubatony",
		Type:      "addon",
		Kind:      reconcilehealth.KindImmutableVCT,
		Action:    reconcilehealth.ActionOrphanRecreate,
		Safe:      true,
	}
	res, err := r.Apply(context.Background(), iss, "ivo", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied {
		t.Error("expected Applied=true")
	}

	// The StatefulSet must be gone.
	if _, err := cs.AppsV1().StatefulSets(ns).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
		t.Error("StatefulSet should have been deleted")
	}
	// The CR must carry the force-reconcile annotation with our fixed stamp.
	got, err := dyn.Resource(kube.GVRAddons).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched addon: %v", err)
	}
	anns := got.GetAnnotations()
	if anns["kuso.sislelabs.com/force-reconcile"] != "1700000000" {
		t.Errorf("force-reconcile annotation = %q, want 1700000000", anns["kuso.sislelabs.com/force-reconcile"])
	}
	// An audit entry must have been emitted.
	if len(aud.entries) != 1 || aud.entries[0].Action != "remediate.orphan_recreate" {
		t.Errorf("expected one orphan_recreate audit entry, got %+v", aud.entries)
	}
}

func TestApply_OrphanRecreate_IdempotentWhenSTSAbsent(t *testing.T) {
	const ns, name = "kuso", "scubatony-db"
	cs := kubefake.NewSimpleClientset() // no STS seeded
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRAddons:       "KusoAddonList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	_, _ = dyn.Resource(kube.GVRAddons).Namespace(ns).Create(context.Background(), newUnstructuredAddon(ns, name), metav1.CreateOptions{})
	r := &Remediator{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Now: fixedClock}
	iss := reconcilehealth.Issue{Resource: name, Namespace: ns, Type: "addon", Action: reconcilehealth.ActionOrphanRecreate, Safe: true}
	res, err := r.Apply(context.Background(), iss, "ivo", false)
	if err != nil {
		t.Fatalf("Apply should be idempotent when STS absent: %v", err)
	}
	if !res.Applied {
		t.Error("expected Applied=true (reconcile still triggered)")
	}
}
