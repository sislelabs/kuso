package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// DATA-9: DELETE with the pre-qualified name resolved the CR but compared
// subscriptions against "alpha-db" / "alpha-alpha-db", so a service
// subscribed as "db" kept it and a later re-add re-mounted it.
func TestDelete_FQNNameStillUnsubscribes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList", kube.GVRServices: "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList", kube.GVRAddons: "KusoAddonList",
	})
	for _, sd := range []seed{
		seedProj("alpha"),
		seedPlainAddon("alpha", "db"),
		typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web", Labels: map[string]string{kube.LabelProject: "alpha"}},
			Spec:       kube.KusoServiceSpec{Project: "alpha", SubscribedAddons: []string{"db", "cache"}},
		}),
	} {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, Namespace: "kuso"}

	if err := s.Delete(ctx, "alpha", "alpha-db"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	svc, err := dyn.Resource(kube.GVRServices).Namespace("kuso").Get(ctx, "alpha-web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subs, _, _ := unstructured.NestedStringSlice(svc.Object, "spec", "subscribedAddons")
	if !slices.Equal(subs, []string{"cache"}) {
		t.Fatalf("subscribedAddons = %v, want [cache]", subs)
	}
}

// DATA-9: when the instance admin DSN is unreadable the clone's database
// is never dropped. Deleting its conn Secret anyway erased the only trail
// the orphan_conn_secret health check keys on.
func TestDelete_InstanceCloneKeepsConnWhenDropSkipped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-db-pr-3", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", "kuso.sislelabs.com/preview-pr": "3"},
		},
		Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres", UseInstanceAddon: "pg"},
	}))
	// No kuso-instance-shared Secret: instanceAdminDSN fails.
	cs := kubefake.NewSimpleClientset(connSecret("alpha-db-pr-3-conn"))
	s.Kube.Clientset = cs

	if err := s.Delete(ctx, "alpha", "db-pr-3"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-db-pr-3-conn", metav1.GetOptions{}); err != nil {
		t.Errorf("conn secret of an undropped database was deleted: %v", err)
	}
}
