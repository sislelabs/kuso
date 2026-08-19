package handlers

// portforward_owned_test.go covers the ownership guard on the addon
// port-forward target resolution — the most privileged data-access path
// in the product (a raw TCP channel into a managed database). Same
// attack shape as the web-UI proxy: overlapping project names ("alpha"
// vs "alpha-bar") + a pre-qualified addon name ("alpha-bar-pg") used to
// resolve the sibling project's Service with no ownership re-check, and
// the audit row recorded the ATTACKER-SUPPLIED project. resolveAddonTarget
// must go through addons.Service.GetOwned and report the resolved CR's
// true owner for the audit trail.

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

func portForwardHandlerWith(t *testing.T, addonCRs []*kube.KusoAddon, objs ...runtime.Object) *PortForwardWSHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList",
	})
	for _, a := range addonCRs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(a)
		if err != nil {
			t.Fatalf("encode addon: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
			t.Fatalf("seed addon: %v", err)
		}
	}
	cs := kubefake.NewSimpleClientset(objs...)
	return &PortForwardWSHandler{
		Svc:  addons.New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso"),
		Kube: &kube.Client{Clientset: cs},
	}
}

func TestResolveAddonTarget_CrossProjectQualifiedNameRejected(t *testing.T) {
	t.Parallel()
	// The victim's Service + Ready pod exist and would resolve fine —
	// only the ownership check stands between the attacker and the
	// tunnel.
	h := portForwardHandlerWith(t,
		[]*kube.KusoAddon{{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-bar-pg"},
			Spec:       kube.KusoAddonSpec{Project: "alpha-bar", Kind: "postgres"},
		}},
		pfService("alpha-bar-pg"), pfReadyPod("alpha-bar-pg-0", "alpha-bar-pg"),
	)
	_, _, _, _, err := h.resolveAddonTarget(context.Background(), "alpha", "alpha-bar-pg")
	if !errors.Is(err, addons.ErrNotFound) {
		t.Fatalf("resolveAddonTarget(alpha, alpha-bar-pg): got %v, want addons.ErrNotFound", err)
	}
}

func TestResolveAddonTarget_OwnerResolvesAndAuditGetsTrueOwner(t *testing.T) {
	t.Parallel()
	h := portForwardHandlerWith(t,
		[]*kube.KusoAddon{{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-bar-pg"},
			Spec:       kube.KusoAddonSpec{Project: "alpha-bar", Kind: "postgres"},
		}},
		pfService("alpha-bar-pg"), pfReadyPod("alpha-bar-pg-0", "alpha-bar-pg"),
	)
	for _, name := range []string{"pg", "alpha-bar-pg"} { // short + qualified forms
		ns, pod, port, owner, err := h.resolveAddonTarget(context.Background(), "alpha-bar", name)
		if err != nil {
			t.Fatalf("resolveAddonTarget(alpha-bar, %q): %v", name, err)
		}
		if ns != "kuso" || pod != "alpha-bar-pg-0" || port != 5432 {
			t.Errorf("resolveAddonTarget(%q) = (%s, %s, %d), want (kuso, alpha-bar-pg-0, 5432)", name, ns, pod, port)
		}
		// The audit row consumes this: it must be the CR's spec.project,
		// never a caller-controlled string.
		if owner != "alpha-bar" {
			t.Errorf("owner = %q, want alpha-bar (the resolved CR's spec.project)", owner)
		}
	}
}

func TestResolveAddonTarget_MissingAddonIsNotFound(t *testing.T) {
	t.Parallel()
	h := portForwardHandlerWith(t, nil)
	_, _, _, _, err := h.resolveAddonTarget(context.Background(), "alpha", "pg")
	if !errors.Is(err, addons.ErrNotFound) {
		t.Fatalf("got %v, want addons.ErrNotFound", err)
	}
}

// pfService is a minimal addon Service: one port targeting 5432 with a
// pod selector, in the home namespace.
func pfService(name string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 5432, TargetPort: intstr.FromInt32(5432)}},
		},
	}
}

// pfReadyPod is a Running+Ready pod matching pfService's selector.
func pfReadyPod(name, app string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels: map[string]string{"app": app},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}
