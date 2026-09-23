package builds

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func helmReleaseSecret(release string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1." + release + ".v1",
			Namespace: "kuso",
			Labels:    map[string]string{"owner": "helm", "name": release},
		},
		Type: "helm.sh/release.v1",
	}
}

func orphanSweepClient(t *testing.T, releases []string, seeds ...seed) (*kube.Client, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	var objs []runtime.Object
	for _, r := range releases {
		objs = append(objs, helmReleaseSecret(r))
	}
	cs := fake.NewSimpleClientset(objs...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	})
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, "kuso"); err != nil {
			t.Fatalf("seed %s/%s: %v", s.gvr.Resource, s.obj.GetName(), err)
		}
	}
	return &kube.Client{Clientset: cs, Dynamic: dyn}, dyn
}

func remainingReleaseSecrets(t *testing.T, kc *kube.Client) map[string]bool {
	t.Helper()
	l, err := kc.Clientset.CoreV1().Secrets("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range l.Items {
		out[s.Labels["name"]] = true
	}
	return out
}

func TestSweepOrphanHelmReleases_KeepsLiveRunRelease(t *testing.T) {
	t.Parallel()
	run := typedSeed(kube.GVRRuns, "KusoRun", &kube.KusoRun{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-run-1", Namespace: "kuso"},
	})
	kc, _ := orphanSweepClient(t, []string{"alpha-run-1", "gone-addon"}, run)

	n, err := SweepOrphanHelmReleases(context.Background(), kc, "kuso", nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	left := remainingReleaseSecrets(t, kc)
	if !left["alpha-run-1"] {
		t.Errorf("live KusoRun's helm release was deleted")
	}
	if left["gone-addon"] || n != 1 {
		t.Errorf("true orphan should be swept: deleted=%d left=%v", n, left)
	}
}

func TestSweepOrphanHelmReleases_ListFailureSkipsSweep(t *testing.T) {
	t.Parallel()
	addon := typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg", Namespace: "kuso"},
	})
	kc, dyn := orphanSweepClient(t, []string{"alpha-pg", "gone-addon"}, addon)
	dyn.PrependReactor("list", "kusoaddons", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver timeout")
	})

	n, err := SweepOrphanHelmReleases(context.Background(), kc, "kuso", nil)
	if err == nil {
		t.Errorf("expected an error when a live-owner LIST fails")
	}
	left := remainingReleaseSecrets(t, kc)
	if n != 0 || !left["alpha-pg"] || !left["gone-addon"] {
		t.Errorf("a failed LIST must skip the whole sweep: deleted=%d left=%v", n, left)
	}
}

// The live-owner set must cover every kind the helm-operator renders a
// release for, or that kind's releases are reaped as orphans daily.
func TestOrphanSweepOwnerKindsCoverOperatorWatches(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../operator/watches.yaml")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, g := range helmReleaseOwnerGVRs {
		have[g.Resource] = true
	}
	kinds := regexp.MustCompile(`(?m)^\s*kind:\s*(\w+)\s*$`).FindAllStringSubmatch(string(raw), -1)
	if len(kinds) == 0 {
		t.Fatal("no kinds parsed from watches.yaml")
	}
	for _, m := range kinds {
		res := strings.ToLower(m[1]) + "s"
		if !have[res] {
			t.Errorf("watched kind %s (%s) missing from helmReleaseOwnerGVRs", m[1], res)
		}
	}
}
