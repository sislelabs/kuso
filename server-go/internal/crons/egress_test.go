package crons

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

// egressFixture seeds service alpha-web (with the given egress spec) plus
// its production env. The env mirror is deliberately left at the zero
// value so a test passing only because it read the env would fail.
func egressFixture(t *testing.T, private, platform bool, extra ...*kube.KusoCron) *Service {
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
	seed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web"},
		Spec:       kube.KusoServiceSpec{Project: "alpha", PrivateEgress: private, PlatformAPIEgress: platform},
	})
	seed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web",
			Image: &kube.KusoImage{Repository: "registry.local/alpha/web", Tag: "sha1"},
		},
	})
	for _, c := range extra {
		seed(kube.GVRCrons, "KusoCron", c)
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
}

func TestAdd_CopiesServiceEgress(t *testing.T) {
	cases := []struct{ private, platform bool }{{true, false}, {false, true}, {false, false}}
	for _, tc := range cases {
		s := egressFixture(t, tc.private, tc.platform)
		cr, err := s.Add(context.Background(), "alpha", "web", CreateCronRequest{
			Name: "nightly", Schedule: "0 0 * * *", Command: []string{"echo"},
		})
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if cr.Spec.PrivateEgress != tc.private || cr.Spec.PlatformAPIEgress != tc.platform {
			t.Errorf("service egress private=%v platform=%v, cron got private=%v platform=%v",
				tc.private, tc.platform, cr.Spec.PrivateEgress, cr.Spec.PlatformAPIEgress)
		}
	}
}

func TestSyncFromService_RestampsEgress(t *testing.T) {
	stale := &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-nightly", Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project: "alpha", Service: "alpha-web", Schedule: "0 0 * * *", Command: []string{"echo"},
		},
	}
	s := egressFixture(t, true, true, stale)
	cr, err := s.SyncFromService(context.Background(), "alpha", "web", "nightly")
	if err != nil {
		t.Fatalf("SyncFromService: %v", err)
	}
	if !cr.Spec.PrivateEgress || !cr.Spec.PlatformAPIEgress {
		t.Errorf("sync must restamp egress from the service, got private=%v platform=%v",
			cr.Spec.PrivateEgress, cr.Spec.PlatformAPIEgress)
	}
}
