package addons

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// DATA-8: the in-use port set came from the informer cache, which lags the
// port the previous enable just wrote, so back-to-back enables for two
// addons both got the lowest free port. A stopped cache is the worst case
// of that lag: it stays "synced" and never sees the first write.
func TestEnablePublicTCP_BackToBackGetDistinctPortsDespiteStaleCache(t *testing.T) {
	t.Setenv("KUSO_TCP_PROXY_PORTS", "30000-30010")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRKuso: "KusoList", kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList", kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons: "KusoAddonList", kube.GVRBuilds: "KusoBuildList",
		kube.GVRCrons: "KusoCronList", kube.GVRRuns: "KusoRunList",
	})
	for _, sd := range []seed{
		seedProj("alpha"),
		seedAddonSpec("alpha", "a", kube.KusoAddonSpec{Kind: "postgres"}),
		seedAddonSpec("alpha", "b", kube.KusoAddonSpec{Kind: "postgres"}),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	k := &kube.Client{Dynamic: dyn}
	k.Cache = kube.NewCache(k)
	k.Cache.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !k.Cache.WaitForSync(ctx) {
		t.Fatal("cache never synced")
	}
	k.Cache.Stop()
	s := &Service{Kube: k, Namespace: "kuso"}

	pa, err := s.EnablePublicTCP(context.Background(), "alpha", "a")
	if err != nil {
		t.Fatalf("enable a: %v", err)
	}
	pb, err := s.EnablePublicTCP(context.Background(), "alpha", "b")
	if err != nil {
		t.Fatalf("enable b: %v", err)
	}
	if pa == pb {
		t.Fatalf("both addons got port %d", pa)
	}
}
