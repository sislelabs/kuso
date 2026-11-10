package buildcontroller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// A KusoBuild added to the informer reaches ensureJob through the work
// queue, off the informer callback.
func TestInformerEventReconcilesViaQueue(t *testing.T) {
	ns := "kuso-queue-e2e"
	cs := kubefake.NewSimpleClientset(managedNS(ns))
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso: "KusoList", kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList", kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons: "KusoAddonList", kube.GVRBuilds: "KusoBuildList",
		kube.GVRCrons: "KusoCronList", kube.GVRRuns: "KusoRunList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	kc.Cache = kube.NewCache(kc)
	kc.Cache.Start()
	t.Cleanup(kc.Cache.Stop)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Service{Kube: kc, Cache: kc.Cache, Logger: retryTestLogger()}
	s.Start(ctx)
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	defer scancel()
	if !kc.Cache.WaitForSync(sctx) {
		t.Fatal("cache sync timed out")
	}

	if _, err := dyn.Resource(kube.GVRBuilds).Namespace(ns).Create(ctx, retryTestBuild(ns, "b-q"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := cs.BatchV1().Jobs(ns).Get(ctx, "b-q", metav1.GetOptions{})
		return err == nil
	}) {
		t.Fatal("Job never created from the informer event")
	}
}
