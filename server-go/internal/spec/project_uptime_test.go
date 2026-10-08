package spec

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// A project re-created from `kuso export` must keep its uptime opt-out.
func TestProjectUptime_ExportAndApply(t *testing.T) {
	k, ns := fakeKube(t, typedPlanSeed(kube.GVRProjects, "KusoProject", "shop", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{Uptime: &kube.KusoProjectUptime{Disabled: true}},
	}))
	f, err := Export(context.Background(), k, ns, "shop")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if f.Uptime == nil || !f.Uptime.Disabled {
		t.Fatalf("export dropped project uptime: %+v", f.Uptime)
	}

	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	if _, err := r.Apply(context.Background(), &Plan{}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.projectUpdates) != 1 || fp.projectUpdates[0].Uptime == nil || !*fp.projectUpdates[0].Uptime.Disabled {
		t.Fatalf("project updates = %+v", fp.projectUpdates)
	}

	// No block: leave the live setting alone.
	fp = &fakeProjects{}
	r.Projects = fp
	if _, err := r.Apply(context.Background(), &Plan{}, &File{Project: "shop"}, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.projectUpdates) != 0 {
		t.Fatalf("absent uptime block wrote the project: %+v", fp.projectUpdates)
	}
}
