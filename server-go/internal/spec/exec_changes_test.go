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
		Spec: kube.KusoServiceSpec{Project: "shop", Runtime: "dockerfile", Port: 8080, Command: []string{"./run"},
			EnvVars: []kube.KusoEnvVar{{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512"}}},
	})
	keepEnv := map[string]EnvValue{"NODE_OPTIONS": {Value: "--max-old-space-size=512"}}
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
		{Name: "api", Runtime: "dockerfile", Port: 9090, Command: []string{"./run"}, Env: keepEnv},
	}})
	if len(same.ExecChanges) != 0 {
		t.Errorf("unchanged command/env flagged: %+v", same.ExecChanges)
	}

	changed := plan(&File{Project: "shop", Services: []ServiceSpec{
		{Name: "api", Runtime: "dockerfile", Port: 8080, Command: []string{"sh", "-c", "printenv"}, Env: keepEnv},
		{Name: "tool", Runtime: "image", Port: 8080, Image: &ImageSpec{Repository: "busybox"}},
		{Name: "web", Runtime: "dockerfile", Port: 8080, Env: map[string]EnvValue{"API_URL": {Value: "x"}}},
		{Name: "job", Runtime: "dockerfile", Port: 8080, Env: map[string]EnvValue{"NIXPACKS_START_CMD": {Value: "env"}}},
	}})
	got := map[string]bool{}
	for _, c := range changed.ExecChanges {
		got[c.Resource] = true
	}
	if !got["service:api"] || !got["service:tool"] || !got["service:job"] || got["service:web"] || len(got) != 3 {
		t.Errorf("ExecChanges = %+v, want service:api, service:job and service:tool only", changed.ExecChanges)
	}

	envOnly := plan(&File{Project: "shop", Services: []ServiceSpec{
		{Name: "api", Runtime: "dockerfile", Port: 8080, Command: []string{"./run"},
			Env: map[string]EnvValue{"NODE_OPTIONS": {Value: "--import=data:text/javascript,1"}}},
	}})
	if len(envOnly.ExecChanges) != 1 || envOnly.ExecChanges[0].Fields[0] != "env.NODE_OPTIONS" {
		t.Errorf("changed NODE_OPTIONS: ExecChanges = %+v, want env.NODE_OPTIONS", envOnly.ExecChanges)
	}
}
