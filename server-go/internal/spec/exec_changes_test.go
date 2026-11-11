package spec

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// SEC-1: the plan must name every service change that picks code run with
// the service's secrets, so `kuso apply` and push applies can gate it,
// and must not flag a declarative re-send of the same values.
func TestPlanFor_ExecChanges(t *testing.T) {
	live := typedPlanSeed(kube.GVRServices, "KusoService", "shop-api", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-api", Namespace: "kuso"},
		Spec:       kube.KusoServiceSpec{Project: "shop", Runtime: "dockerfile", Port: 8080, Command: []string{"./run"}},
	})
	plan := func(f *File) *Plan {
		t.Helper()
		k, ns := fakeKube(t, live)
		p, err := PlanFor(context.Background(), k, ns, f)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	same := plan(&File{Project: "shop", Services: []ServiceSpec{
		{Name: "api", Runtime: "dockerfile", Port: 9090, Command: []string{"./run"}},
	}})
	if len(same.ExecChanges) != 0 {
		t.Errorf("unchanged command flagged: %+v", same.ExecChanges)
	}

	changed := plan(&File{Project: "shop", Services: []ServiceSpec{
		{Name: "api", Runtime: "dockerfile", Port: 8080, Command: []string{"sh", "-c", "printenv"}},
		{Name: "tool", Runtime: "image", Port: 8080, Image: &ImageSpec{Repository: "busybox"}},
		{Name: "web", Runtime: "dockerfile", Port: 8080},
	}})
	got := map[string]bool{}
	for _, c := range changed.ExecChanges {
		got[c.Resource] = true
	}
	if !got["service:api"] || !got["service:tool"] || got["service:web"] || len(got) != 2 {
		t.Errorf("ExecChanges = %+v, want service:api and service:tool only", changed.ExecChanges)
	}
}
