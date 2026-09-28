package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestPatchService_WaitForCI(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("alpha", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("alpha", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "web"},
			},
			Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000, Runtime: "dockerfile"},
		}),
	)
	for _, want := range []bool{true, false} {
		svc, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{WaitForCI: &want})
		if err != nil {
			t.Fatalf("PatchService: %v", err)
		}
		if svc.Spec.WaitForCI != want {
			t.Errorf("waitForCI = %v after patching %v", svc.Spec.WaitForCI, want)
		}
	}
	// Omitting the field leaves it alone.
	on := true
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{WaitForCI: &on}); err != nil {
		t.Fatal(err)
	}
	port := int32(8080)
	svc, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Port: &port})
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Spec.WaitForCI {
		t.Error("unrelated patch reset waitForCI")
	}
}
