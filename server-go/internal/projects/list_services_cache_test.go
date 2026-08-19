package projects

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// TestListServicesForProject_ServesFromInformerCache pins the W1 fix:
// the per-dashboard-card Describe path must read services through the
// cached typed-list helper (kube/crds.go list[T] → informer cache), not
// a live Dynamic LIST against the apiserver.
//
// Fake-client trap: a fake dynamic client can't distinguish "cached"
// from "live" by data alone — both return the seeded object. So we test
// via the client seam instead: after the informer has synced, every
// subsequent LIVE list is made to fail via a prepended reactor. If
// listServicesForProject still succeeds, it can only have been served
// from the informer cache. A regression back to
// Dynamic.Resource(GVRServices).List would hit the reactor and fail.
func TestListServicesForProject_ServesFromInformerCache(t *testing.T) {
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
	for _, sd := range []seed{
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile"}),
		// A sibling project's service proves the label selector still
		// filters correctly on the cached (client-side-filter) path.
		seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "y"}}),
		seedService("beta", "api", kube.KusoServiceSpec{Runtime: "dockerfile"}),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}

	kc := &kube.Client{Dynamic: dyn}
	kc.Cache = kube.NewCache(kc)
	kc.Cache.Start()
	t.Cleanup(kc.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !kc.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	// From here on, any LIVE list of services fails loudly. The informer
	// already holds its snapshot, so a cache-served read is unaffected.
	dyn.PrependReactor("list", kube.GVRServices.Resource,
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("live apiserver LIST issued — expected informer-cache read")
		})

	s := New(kc, "kuso")
	got, err := s.listServicesForProject(ctx, "alpha")
	if err != nil {
		t.Fatalf("listServicesForProject after live-LIST cutoff: %v (regression to a cache-bypassing live list?)", err)
	}
	if len(got) != 1 || got[0].Name != serviceCRName("alpha", "web") {
		t.Fatalf("got %d services %+v, want exactly alpha-web", len(got), names(got))
	}

	// Belt and braces: the same call with NO cache wired must hit the
	// reactor — proving the reactor actually guards the live path and
	// the pass above wasn't vacuous.
	sNoCache := New(&kube.Client{Dynamic: dyn}, "kuso")
	if _, err := sNoCache.listServicesForProject(ctx, "alpha"); err == nil {
		t.Fatal("cache-less client should have hit the failing live-LIST reactor")
	}
}

func names(svcs []kube.KusoService) []string {
	out := make([]string, len(svcs))
	for i := range svcs {
		out[i] = svcs[i].Name
	}
	return out
}
