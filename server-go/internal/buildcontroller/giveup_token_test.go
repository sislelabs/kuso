package buildcontroller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// F16: giving up is terminal, and a build whose Job was never created has
// no Job TTL to reap its clone-token Secret — it would live forever.
func TestGiveUpDeletesCloneToken(t *testing.T) {
	ns := "kuso-giveup-token"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRBuilds: "KusoBuildList",
	})
	u := retryTestBuild(ns, "b7")
	if err := dyn.Tracker().Create(kube.GVRBuilds, u, ns); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: builds.CloneTokenSecretName("b7"), Namespace: ns},
	})
	s := &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Logger: retryTestLogger()}

	s.giveUp(context.Background(), u, retryMaxAttempts)

	if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), builds.CloneTokenSecretName("b7"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("clone-token secret survived giveUp (err=%v)", err)
	}
}
