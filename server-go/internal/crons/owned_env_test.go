package crons

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// overlapFixture: project "a" owns service "b" (a-b, deployed); project
// "a-b" owns service "c" (a-b-c). "a" + "b-c" concatenates to the victim's
// names, and a cron under "a" must not copy the victim's image or secrets.
func overlapFixture(t *testing.T, extra ...*kube.KusoCron) *Service {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRCrons:        "KusoCronList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	seed := func(gvr schema.GroupVersionResource, kind string, obj any) {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatalf("seed %s: %v", kind, err)
		}
	}
	for _, p := range []struct{ project, svc string }{{"a", "a-b"}, {"a-b", "a-b-c"}} {
		seed(kube.GVRServices, "KusoService", &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: p.svc},
			Spec:       kube.KusoServiceSpec{Project: p.project},
		})
		seed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: p.svc + "-production"},
			Spec: kube.KusoEnvironmentSpec{
				Project: p.project, Service: p.svc,
				Image:          &kube.KusoImage{Repository: "registry.local/" + p.svc, Tag: "sha1"},
				EnvFromSecrets: []string{p.svc + "-secrets"},
			},
		})
	}
	for _, c := range extra {
		seed(kube.GVRCrons, "KusoCron", c)
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

func TestAdd_RefusesAnotherProjectsProductionEnv(t *testing.T) {
	t.Parallel()
	s := overlapFixture(t)
	_, err := s.Add(context.Background(), "a", "b-c", CreateCronRequest{
		Name: "dump", Schedule: "0 0 * * *", Command: []string{"env"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Add(a, b-c) = %v, want the not-deployed ErrInvalid", err)
	}
}

// Sync must re-resolve from the cron's own service, not from whatever the
// caller's service string concatenates to.
func TestSyncFromService_UsesTheCronsOwnService(t *testing.T) {
	t.Parallel()
	// Project a's cron on service "b", named "c-nightly": CRName(a, b, c-nightly)
	// == CRName(a, b-c, nightly) == "a-b-c-nightly".
	own := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-nightly", Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project: "a", Service: "a-b", Schedule: "0 0 * * *", Command: []string{"echo"},
		},
	}
	s := overlapFixture(t, own)
	cr, err := s.SyncFromService(context.Background(), "a", "b-c", "nightly")
	if err != nil {
		t.Fatalf("SyncFromService: %v", err)
	}
	if cr.Spec.Image == nil || cr.Spec.Image.Repository != "registry.local/a-b" {
		t.Fatalf("cron synced image %+v, want its own service's", cr.Spec.Image)
	}
	for _, sec := range cr.Spec.EnvFromSecrets {
		if sec == "a-b-c-secrets" {
			t.Fatalf("cron mounted another project's secrets: %v", cr.Spec.EnvFromSecrets)
		}
	}
}
