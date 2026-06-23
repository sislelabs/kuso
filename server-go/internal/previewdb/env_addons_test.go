package previewdb

import (
	"context"
	"log/slog"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

// newTestCloner builds a Cloner backed by a fake dynamic client, seeded with a
// project + the given addon CRs. The Cloner's Addons service shares the same fake
// client, so EnsureEnvAddons' List/Add/Get all hit the same store.
func newTestCloner(t *testing.T, project string, addonCRs ...*kube.KusoAddon) (*Cloner, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
	})
	mustSeed(t, dyn, kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: project, Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}},
	})
	for _, a := range addonCRs {
		mustSeed(t, dyn, kube.GVRAddons, "KusoAddon", a)
	}
	k := &kube.Client{Dynamic: dyn}
	return &Cloner{
		Kube:    k,
		Addons:  addons.New(k, "kuso"),
		Logger:  slog.Default(),
		BaseCtx: context.Background(),
	}, dyn
}

func mustSeed(t *testing.T, dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, kind string, obj any) {
	t.Helper()
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
		t.Fatalf("seed %s: %v", kind, err)
	}
}

func addonCR(project, short, kind string) *kube.KusoAddon {
	return &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + short,
			Namespace: "kuso",
			// addons.List filters by the project label, so a seeded addon must
			// carry it to be discoverable.
			Labels: map[string]string{kube.LabelProject: project},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: kind},
	}
}

// TestEnsureEnvAddons_NamesLabelsKinds verifies that a named env clones every
// requested stateful kind, names each clone "<short>-<env>", labels it env=<env>,
// and returns the clones' conn-secret names. Non-requested kinds are skipped.
func TestEnsureEnvAddons_NamesLabelsKinds(t *testing.T) {
	c, dyn := newTestCloner(t, "alpha",
		addonCR("alpha", "pg", "postgres"),
		addonCR("alpha", "cache", "redis"),
		addonCR("alpha", "files", "s3"),
	)

	conns, err := c.EnsureEnvAddons(context.Background(), "alpha", "staging", EnvAddonOpts{
		Kinds: []string{"postgres", "redis"}, // NOT s3 → s3 must be skipped
	})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}

	wantConns := []string{"alpha-cache-staging-conn", "alpha-pg-staging-conn"}
	got := append([]string(nil), conns...)
	sort.Strings(got)
	if len(got) != len(wantConns) || got[0] != wantConns[0] || got[1] != wantConns[1] {
		t.Fatalf("conns = %v, want %v", got, wantConns)
	}

	// The clones exist with the canonical env label.
	for _, short := range []string{"pg-staging", "cache-staging"} {
		a := getAddon(t, dyn, "alpha-"+short)
		if a == nil {
			t.Fatalf("clone alpha-%s not created", short)
		}
		if a.Labels[kube.LabelEnv] != "staging" {
			t.Fatalf("clone alpha-%s env label = %q, want staging", short, a.Labels[kube.LabelEnv])
		}
		// Named-env clones carry NO preview labels.
		if _, ok := a.Labels["kuso.sislelabs.com/preview-pr"]; ok {
			t.Fatalf("clone alpha-%s should not carry a preview-pr label", short)
		}
	}

	// s3 was not requested → no s3 clone.
	if getAddon(t, dyn, "alpha-files-staging") != nil {
		t.Fatalf("s3 clone created but s3 kind was not requested")
	}
}

// TestEnsureEnvAddons_SkipsExistingCloneAndEnvScopedSources verifies idempotency
// (an existing clone is reused, not duplicated) and that an addon already scoped
// to an env (a clone) is never cloned again.
func TestEnsureEnvAddons_SkipsEnvScopedSources(t *testing.T) {
	// Source pg + an already-env-scoped addon (a clone) that must be ignored.
	cloneSrc := addonCR("alpha", "pg-staging", "postgres")
	cloneSrc.Labels[kube.LabelEnv] = "staging" // keep the project label addonCR set
	c, dyn := newTestCloner(t, "alpha",
		addonCR("alpha", "pg", "postgres"),
		cloneSrc,
	)

	conns, err := c.EnsureEnvAddons(context.Background(), "alpha", "qa", EnvAddonOpts{Kinds: []string{"postgres"}})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}
	// Only the real source got cloned → alpha-pg-qa. The env-scoped pg-staging
	// was skipped (no alpha-pg-staging-qa).
	if len(conns) != 1 || conns[0] != "alpha-pg-qa-conn" {
		t.Fatalf("conns = %v, want [alpha-pg-qa-conn]", conns)
	}
	if getAddon(t, dyn, "alpha-pg-staging-qa") != nil {
		t.Fatalf("an env-scoped source was cloned — clone-of-a-clone bug")
	}
}

func getAddon(t *testing.T, dyn *dynamicfake.FakeDynamicClient, name string) *kube.KusoAddon {
	t.Helper()
	u, err := dyn.Resource(kube.GVRAddons).Namespace("kuso").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	var a kube.KusoAddon
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &a); err != nil {
		t.Fatalf("decode addon %s: %v", name, err)
	}
	return &a
}
