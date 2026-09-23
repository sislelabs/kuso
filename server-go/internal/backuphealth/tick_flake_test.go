package backuphealth

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// flakyCluster is a kuso namespace with one scheduled postgres addon whose
// kuso-backup-s3 Secret is missing (an "error" condition), plus the
// kuso-server Deployment that carries the persisted state annotation.
// Setting *flaky makes the addon's kuso-backup-s3 GET fail transiently.
func flakyCluster(t *testing.T) (*kube.Client, *kubefake.Clientset, *bool) {
	t.Helper()
	const ns = "kuso"
	cs := kubefake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: stateCarrier, Namespace: ns},
	})
	flaky := new(bool)
	cs.PrependReactor("get", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if *flaky && a.(k8stesting.GetAction).GetName() == "kuso-backup-s3" {
			return true, nil, errors.New("apiserver timeout")
		}
		return false, nil, nil
	})

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVRProjects: "KusoProjectList",
			kube.GVRAddons:   "KusoAddonList",
		})
	addon := &unstructured.Unstructured{}
	addon.SetGroupVersionKind(schema.GroupVersionKind{
		Group: kube.GVRAddons.Group, Version: kube.GVRAddons.Version, Kind: "KusoAddon",
	})
	addon.SetNamespace(ns)
	addon.SetName("p-pg")
	addon.SetLabels(map[string]string{"kuso.sislelabs.com/project": "p"})
	_ = unstructured.SetNestedField(addon.Object, map[string]any{
		"kind":   "postgres",
		"backup": map[string]any{"schedule": "0 3 * * *"},
	}, "spec")
	if err := dyn.Tracker().Create(kube.GVRAddons, addon, ns); err != nil {
		t.Fatal(err)
	}
	return &kube.Client{Clientset: cs, Dynamic: dyn}, cs, flaky
}

func persisted(t *testing.T, cs *kubefake.Clientset) string {
	t.Helper()
	d, err := cs.AppsV1().Deployments("kuso").Get(context.Background(), stateCarrier, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return d.Annotations[StateAnnotation]
}

// emits counts state transitions: tick persists the state exactly once per
// notification, so every patch of the carrier Deployment is one alert.
func emits(cs *kubefake.Clientset) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == "patch" && a.GetResource().Resource == "deployments" {
			n++
		}
	}
	return n
}

// newProcess mirrors Run's boot: a fresh Watcher seeded from the annotation.
func newProcess(kc *kube.Client) *Watcher {
	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.New(slog.DiscardHandler)}
	w.lastState = w.loadState(context.Background())
	return w
}

// A single failed apiserver read must not move the state. It used to drop
// severity to warn (and after a restart drop the addon part), then the
// next clean tick moved it back: two spurious alerts per flake.
func TestTick_FailedReadKeepsPreviousState(t *testing.T) {
	ctx := context.Background()

	t.Run("after restart", func(t *testing.T) {
		kc, cs, flaky := flakyCluster(t)
		newProcess(kc).tick(ctx)
		want := persisted(t, cs)
		if want == "" {
			t.Fatal("setup: the missing Secret should have produced an unhealthy state")
		}

		before := emits(cs)

		w := newProcess(kc)
		*flaky = true
		w.tick(ctx)
		*flaky = false
		w.tick(ctx)
		if got := persisted(t, cs); got != want {
			t.Fatalf("state moved %q -> %q", want, got)
		}
		if n := emits(cs) - before; n != 0 {
			t.Fatalf("restarted process emitted %d alerts for an unchanged condition", n)
		}
	})

	t.Run("same process", func(t *testing.T) {
		kc, cs, flaky := flakyCluster(t)
		w := newProcess(kc)
		w.tick(ctx)
		want := persisted(t, cs)
		before := emits(cs)

		*flaky = true
		w.tick(ctx)
		*flaky = false
		w.tick(ctx)
		if got := persisted(t, cs); got != want {
			t.Fatalf("state moved %q -> %q", want, got)
		}
		if n := emits(cs) - before; n != 0 {
			t.Fatalf("one failed read emitted %d alerts", n)
		}
	})
}
