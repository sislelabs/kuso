package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Deleting a service must drop its build/run history — the archived
// build records + logs and the live KusoBuild/KusoRun CRs — so a service
// recreated at the same name starts clean instead of showing the dead
// one's deployments. Sibling services' history must survive.
func TestDeleteService_DropsBuildAndRunHistory(t *testing.T) {
	t.Parallel()
	build := func(name, svc string) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", name, &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{labelProject: "alpha"}},
			Spec:       kube.KusoBuildSpec{Project: "alpha", Service: svc},
		})
	}
	run := func(name, svc string) seed {
		return typedSeed(kube.GVRRuns, "KusoRun", name, &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{labelProject: "alpha"}},
			Spec:       kube.KusoRunSpec{Project: "alpha", Service: svc},
		})
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		build("alpha-web-b1", "alpha-web"),
		build("alpha-api-b1", "alpha-api"),
		run("alpha-web-r1", "alpha-web"),
		run("alpha-api-r1", "alpha-api"),
	)
	var cleaned []string
	s.BuildHistoryCleanupForService = func(_ context.Context, project, service string) error {
		cleaned = append(cleaned, project+"/"+service)
		return nil
	}

	if err := s.DeleteService(context.Background(), "alpha", "web"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "alpha/web" {
		t.Errorf("build history cleanup calls = %v, want [alpha/web]", cleaned)
	}
	ctx := context.Background()
	exists := func(name string, isRun bool) bool {
		g := kube.GVRBuilds
		if isRun {
			g = kube.GVRRuns
		}
		_, err := s.Kube.Dynamic.Resource(g).Namespace("kuso").Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	if exists("alpha-web-b1", false) || exists("alpha-web-r1", true) {
		t.Error("deleted service's KusoBuild/KusoRun CRs survived")
	}
	if !exists("alpha-api-b1", false) || !exists("alpha-api-r1", true) {
		t.Error("sibling service's KusoBuild/KusoRun CRs were deleted")
	}
}
