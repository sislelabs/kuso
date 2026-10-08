package projects

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// /envs and /env-groups are polled every 10s per open project tab; each
// used to do a live service GET per env.
func TestEnvListings_NoPerEnvServiceGet(t *testing.T) {
	t.Parallel()
	seeds := []seed{seedProject("alpha", kube.KusoProjectSpec{})}
	var deps []runtime.Object
	for i := 0; i < 5; i++ {
		svc := fmt.Sprintf("s%d", i)
		env := "alpha-" + svc + "-production"
		seeds = append(seeds,
			seedService("alpha", svc, kube.KusoServiceSpec{Project: "alpha"}),
			seedEnv("alpha", svc, "production", "main", env))
		deps = append(deps, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: env, Namespace: "kuso"}})
	}
	s := fakeServiceWithSecrets(t, deps, seeds...)
	var gets atomic.Int32
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "kusoservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets.Add(1)
		return false, nil, nil
	})
	ctx := context.Background()
	if _, err := s.ListEnvironments(ctx, "alpha"); err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("ListEnvironments: %d live service GETs, want 0", n)
	}
	gets.Store(0)
	groups, err := s.ListEnvGroups(ctx, "alpha")
	if err != nil {
		t.Fatalf("ListEnvGroups: %v", err)
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("ListEnvGroups: %d live service GETs, want 0", n)
	}
	if len(groups) == 0 || len(groups[0].Services) != 5 {
		t.Errorf("groups = %+v", groups)
	}
}
