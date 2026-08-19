package projects

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// These tests pin the LIVE-LIST contract on destructive teardown cascades:
// DeleteEnvGroup and DeleteEnvironment's addon reclaim must enumerate
// children with a raw dynamic-client LIST against the (fake) apiserver,
// NEVER the informer cache. An addon created seconds before the delete may
// not be in the cache yet, and a cold/degraded informer returns
// empty-but-ok — either silently skips teardown and orphans
// StatefulSets/PVCs/live credentials (the v0.21.6/v0.21.7 leak class).
//
// Fake-client seam (same trick as TestListServicesForProject_ServesFromInformerCache,
// inverted): informer-cache reads never touch the client's reaction chain
// (the informer LISTed once, before the reactors were prepended), so a
// recording fall-through reactor observes live LISTs and only live LISTs.
// If a future perf change re-routes these enumerations through the cached
// ListKuso*ByLabels helpers, the recorded live-LIST count drops to zero and
// the test fails.

func liveListFixture(t *testing.T, seeds []seed) (*Service, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	kc := &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}
	kc.Cache = kube.NewCache(kc)
	kc.Cache.Start()
	t.Cleanup(kc.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !kc.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}
	return New(kc, "kuso"), dyn
}

func recordLiveLists(dyn *dynamicfake.FakeDynamicClient, resource string) *atomic.Int32 {
	var n atomic.Int32
	dyn.PrependReactor("list", resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		n.Add(1)
		return false, nil, nil // observe, then fall through to the tracker
	})
	return &n
}

func seedStagingAddon(project, name string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: project, labelEnv: "staging"},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: "postgres"},
	})
}

// TestDeleteEnvGroup_EnumeratesChildrenViaLiveList: with a warm informer
// cache wired, DeleteEnvGroup must still hit the live apiserver for its
// env/service/addon enumerations, and every child must be gone afterwards.
func TestDeleteEnvGroup_EnumeratesChildrenViaLiveList(t *testing.T) {
	const ns = "kuso"
	stagingSvc := typedSeed(kube.GVRServices, "KusoService", "alpha-web-staging", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-staging",
			Namespace: ns,
			Labels:    map[string]string{labelProject: "alpha", labelService: "web-staging", labelEnv: "staging"},
		},
		Spec: kube.KusoServiceSpec{Project: "alpha", Port: 8080},
	})
	s, dyn := liveListFixture(t, []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging-env"),
		stagingSvc,
		seedStagingAddon("alpha", "alpha-pg-staging"),
	})

	envLists := recordLiveLists(dyn, kube.GVREnvironments.Resource)
	svcLists := recordLiveLists(dyn, kube.GVRServices.Resource)
	addonLists := recordLiveLists(dyn, kube.GVRAddons.Resource)

	if err := s.DeleteEnvGroup(context.Background(), "alpha", "staging"); err != nil {
		t.Fatalf("DeleteEnvGroup: %v", err)
	}

	if envLists.Load() == 0 {
		t.Error("env enumeration never hit the live apiserver — served from the informer cache (orphan hazard on cache lag)")
	}
	if svcLists.Load() == 0 {
		t.Error("service enumeration never hit the live apiserver — served from the informer cache (orphan hazard on cache lag)")
	}
	if addonLists.Load() == 0 {
		t.Error("addon enumeration never hit the live apiserver — served from the informer cache (orphan hazard on cache lag)")
	}

	ctx := context.Background()
	for _, check := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{kube.GVREnvironments, "alpha-web-staging-env"},
		{kube.GVRServices, "alpha-web-staging"},
		{kube.GVRAddons, "alpha-pg-staging"},
	} {
		if _, gerr := dyn.Resource(check.gvr).Namespace(ns).Get(ctx, check.name, metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
			t.Errorf("%s %s must be deleted, err=%v", check.gvr.Resource, check.name, gerr)
		}
	}
}

// TestDeleteEnvironment_AddonReclaimEnumeratesViaLiveList: the named-env
// addon reclaim inside DeleteEnvironment must issue a live addon LIST even
// when a warm cache is wired, and the clone addon must be gone afterwards.
// This is the path whose informer-cache detour risked re-opening the
// v0.21.6 preview-PVC leak.
func TestDeleteEnvironment_AddonReclaimEnumeratesViaLiveList(t *testing.T) {
	const ns = "kuso"
	s, dyn := liveListFixture(t, []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging"),
		seedStagingAddon("alpha", "alpha-pg-staging"),
	})

	addonLists := recordLiveLists(dyn, kube.GVRAddons.Resource)

	if err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-staging"); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if addonLists.Load() == 0 {
		t.Error("addon reclaim never hit the live apiserver — served from the informer cache (orphan hazard on cache lag)")
	}
	if _, gerr := dyn.Resource(kube.GVRAddons).Namespace(ns).Get(context.Background(), "alpha-pg-staging", metav1.GetOptions{}); !apierrors.IsNotFound(gerr) {
		t.Errorf("clone addon must be deleted via the live-list reclaim, err=%v", gerr)
	}
}
