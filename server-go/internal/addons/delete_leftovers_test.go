package addons

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

// BLOB-8: a native s3 env clone's -root and -readonly Secrets are helm
// keep. Left behind, re-creating the env made the chart read the old root
// password back into a fresh MinIO.
func TestDelete_NativeS3CloneReclaimsCredentialSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-store-staging", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "staging"},
		},
		Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "s3"},
	}))
	roleSecret := func(name, role string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/role": role}}}
	}
	cs := kubefake.NewSimpleClientset(
		connSecret("alpha-store-staging-conn"),
		roleSecret("alpha-store-staging-root", "s3-root"),
		roleSecret("alpha-store-staging-readonly", "s3-readonly"),
		// The project's own addon: must survive.
		roleSecret("alpha-store-root", "s3-root"),
	)
	s.Kube.Clientset = cs

	if err := s.Delete(ctx, "alpha", "store-staging"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, gone := range []string{"alpha-store-staging-root", "alpha-store-staging-readonly"} {
		if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, gone, metav1.GetOptions{}); err == nil {
			t.Errorf("%s survived the clone delete", gone)
		}
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-store-root", metav1.GetOptions{}); err != nil {
		t.Errorf("the project addon's root Secret was deleted: %v", err)
	}
}
