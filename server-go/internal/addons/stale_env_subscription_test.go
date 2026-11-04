package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func staleSubscriptionService(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList", kube.GVRServices: "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList", kube.GVRAddons: "KusoAddonList",
	})
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	return &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, Namespace: "kuso"}
}

func seedSubscribedService(project, short string, subs []string) seed {
	return typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + short, Labels: map[string]string{kube.LabelProject: project}},
		Spec:       kube.KusoServiceSpec{Project: project, SubscribedAddons: subs},
	})
}

// DATA-08: deleting an addon must clear it from the env CRs' subscription
// mirror too, or re-adding the name re-mounts it where the service no longer
// subscribes.
func TestDelete_ClearsEnvSubscriptionMirror(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := staleSubscriptionService(t,
		seedProj("alpha"),
		seedPlainAddon("alpha", "storage"),
		seedSubscribedService("alpha", "web", []string{"storage", "db"}),
		seedEnvWithSecrets("alpha", "web", "production", "alpha-web-production",
			[]string{"storage", "db"}, []string{"alpha-storage-conn"}),
	)
	if err := s.Delete(ctx, "alpha", "storage"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env.Spec.SubscribedAddons, []string{"db"}) {
		t.Fatalf("env subscribedAddons = %v, want [db]", env.Spec.SubscribedAddons)
	}
}

// DATA-08: an env whose mirror is already stale (deleted before the fix)
// must not get a re-added addon the parent service doesn't subscribe to.
func TestAdd_StaleEnvMirrorDoesNotMountUnsubscribedAddon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := staleSubscriptionService(t,
		seedProj("alpha"),
		seedSubscribedService("alpha", "web", []string{}),
		seedEnvWithSecrets("alpha", "web", "production", "alpha-web-production",
			[]string{"storage"}, nil),
	)
	if _, err := s.Add(ctx, "alpha", CreateAddonRequest{Name: "storage", Kind: "s3"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(env.Spec.EnvFromSecrets, "alpha-storage-conn") {
		t.Fatalf("re-added addon mounted on an env whose service unsubscribed: %v", env.Spec.EnvFromSecrets)
	}
}
