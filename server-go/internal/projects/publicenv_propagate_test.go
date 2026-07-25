package projects

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestPatchService_PublicEnvOnly_ReachesEnvCRs pins the two-consumer
// split that made publicEnv look build-only when it isn't.
//
// builds.Create bakes __KUSO_RUNTIME_<KEY>__ sentinels from the SERVICE
// spec, but the kusoenvironment chart substitutes them at pod start from
// the ENV CR. PatchService set the service field with no changedFields
// entry, so propagateChangedToEnvs early-returned on !any() and the env
// mirror never ran — the browser received literal sentinel strings.
//
// A publicEnv-ONLY patch is the load-bearing case: co-setting any other
// field masks the bug by making any() true for an unrelated reason.
func TestPatchService_PublicEnvOnly_ReachesEnvCRs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("alpha", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("alpha", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "web"},
			},
			Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000, Runtime: "nixpacks"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	publicEnv := []string{"NEXT_PUBLIC_API_URL"}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		PublicEnv: &publicEnv,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !slices.Contains(env.Spec.PublicEnv, "NEXT_PUBLIC_API_URL") {
		t.Errorf("publicEnv-only PATCH did not reach the env CR: got %v — pods would serve raw __KUSO_RUNTIME_* sentinels",
			env.Spec.PublicEnv)
	}
}
