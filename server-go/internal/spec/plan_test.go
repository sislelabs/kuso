package spec

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// fakeKube builds a *kube.Client backed by dynamic/fake, mirroring the
// setup in internal/projects/projects_test.go. Crons are registered so
// PlanFor's KusoCron listing works.
func fakeKube(t *testing.T, seeds ...planSeed) (*kube.Client, string) {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", s.obj.GetName(), err)
		}
	}
	return &kube.Client{Dynamic: dyn}, "kuso"
}

type planSeed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func typedPlanSeed(gvr schema.GroupVersionResource, kind, name string, obj any) planSeed {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: kind})
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	if u.GetName() == "" {
		u.SetName(name)
	}
	return planSeed{gvr: gvr, obj: u}
}

func seedPlanService(project, service string) planSeed {
	name := project + "-" + service
	return typedPlanSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       kube.KusoServiceSpec{Project: project},
	})
}

func seedPlanAddon(project, addon string) planSeed {
	name := project + "-" + addon
	return typedPlanSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       kube.KusoAddonSpec{Project: project},
	})
}

func seedPlanCron(project, cron string) planSeed {
	name := project + "-" + cron
	return typedPlanSeed(kube.GVRCrons, "KusoCron", name, &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec:       kube.KusoCronSpec{Project: project, Schedule: "0 0 * * *"},
	})
}

func TestPlanFor_DiffsCronsAndRoutesDeletesByPrune(t *testing.T) {
	k, ns := fakeKube(t,
		seedPlanService("shop", "old"),
		seedPlanAddon("shop", "staledb"),
		seedPlanCron("shop", "stale-cron"),
	)
	f := &File{
		Project:  "shop",
		Prune:    false,
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Port: 8080}},
		Crons:    []CronSpec{{Name: "nightly", Kind: "http", Schedule: "0 2 * * *", URL: "https://x"}},
	}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToCreate) != 1 || plan.ServicesToCreate[0] != "api" {
		t.Fatalf("want service api created, got %+v", plan.ServicesToCreate)
	}
	if len(plan.CronsToCreate) != 1 || plan.CronsToCreate[0] != "nightly" {
		t.Fatalf("want cron nightly created, got %+v", plan.CronsToCreate)
	}
	// prune is false → stale resources go to WouldDelete, not *ToDelete.
	if len(plan.ServicesToDelete) != 0 || len(plan.AddonsToDelete) != 0 || len(plan.CronsToDelete) != 0 {
		t.Fatalf("prune=false must leave *ToDelete empty: %+v", plan)
	}
	if len(plan.WouldDelete) == 0 {
		t.Fatalf("prune=false must populate WouldDelete: %+v", plan)
	}
}

func TestPlanFor_PruneTrueExecutesDeletes(t *testing.T) {
	k, ns := fakeKube(t,
		seedPlanService("shop", "old"),
		seedPlanAddon("shop", "staledb"),
		seedPlanCron("shop", "stale-cron"),
	)
	f := &File{Project: "shop", Prune: true,
		Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile", Port: 8080}}}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToDelete) == 0 {
		t.Fatalf("prune=true must populate ServicesToDelete: %+v", plan)
	}
	if len(plan.WouldDelete) != 0 {
		t.Fatalf("prune=true must leave WouldDelete empty: %+v", plan)
	}
}
