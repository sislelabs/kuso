package handlers

// backups_owned_test.go covers ownedAddon, the ownership + namespace
// chokepoint every addon-scoped backup/SQL route resolves through. The
// cross-project shape it must block: project names overlap ("alpha" vs
// "alpha-bar"), so an alpha-authorized caller passing the pre-qualified
// addon name "alpha-bar-pg" would — under raw CRName() string mapping —
// resolve to alpha-bar's CR and conn secret.

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

func backupsHandlerWithAddons(t *testing.T, addonCRs ...*kube.KusoAddon) *BackupsHandler {
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
	return &BackupsHandler{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

func TestOwnedAddon_CrossProjectQualifiedNameRejected(t *testing.T) {
	t.Parallel()
	h := backupsHandlerWithAddons(t, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-bar-pg"},
		Spec:       kube.KusoAddonSpec{Project: "alpha-bar", Kind: "postgres"},
	})
	// alpha-authorized caller reaching for alpha-bar's addon via the
	// pre-qualified name must get the same not-found as a missing addon.
	if _, _, err := h.ownedAddon(context.Background(), "alpha", "alpha-bar-pg"); !errors.Is(err, addons.ErrNotFound) {
		t.Fatalf("got %v, want addons.ErrNotFound", err)
	}
}

func TestOwnedAddon_OwnResolves(t *testing.T) {
	t.Parallel()
	h := backupsHandlerWithAddons(t, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg"},
		Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "postgres"},
	})
	for _, name := range []string{"pg", "alpha-pg"} { // short + qualified forms
		cr, ns, err := h.ownedAddon(context.Background(), "alpha", name)
		if err != nil {
			t.Fatalf("ownedAddon(%q): %v", name, err)
		}
		if cr.Name != "alpha-pg" || ns != "kuso" {
			t.Errorf("ownedAddon(%q) = (%s, %s), want (alpha-pg, kuso)", name, cr.Name, ns)
		}
	}
}

func TestOwnedAddon_MissingIsNotFound(t *testing.T) {
	t.Parallel()
	h := backupsHandlerWithAddons(t)
	if _, _, err := h.ownedAddon(context.Background(), "alpha", "pg"); !errors.Is(err, addons.ErrNotFound) {
		t.Fatalf("got %v, want addons.ErrNotFound", err)
	}
}
