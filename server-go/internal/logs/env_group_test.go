package logs

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// `kuso logs puncho web --env staging` 404'd: the staging env lives on the
// env-group clone web-staging as puncho-web-staging-production.
func TestTail_EnvGroupResolvesToClone(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	seedCR(t, dyn, kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "puncho-web-staging-production", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "puncho", kube.LabelEnv: "staging"}},
		Spec: kube.KusoEnvironmentSpec{Project: "puncho", Service: "puncho-web-staging"},
	})
	s := &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, Namespace: "kuso"}
	ctx := context.Background()

	for _, tc := range []struct{ service, env string }{{"web", "staging"}, {"web-staging", "production"}} {
		_, envName, err := s.Tail(ctx, "puncho", tc.service, tc.env, 10)
		if err != nil || envName != "puncho-web-staging-production" {
			t.Errorf("%s --env %s: env=%q err=%v", tc.service, tc.env, envName, err)
		}
	}
	if _, envName, err := s.Tail(ctx, "puncho", "web", "qa", 10); !errors.Is(err, ErrNotFound) || envName != "puncho-web-qa" {
		t.Errorf("missing env: env=%q err=%v, want puncho-web-qa + ErrNotFound", envName, err)
	}
}
