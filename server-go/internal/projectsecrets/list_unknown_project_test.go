package projectsecrets

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

// An unknown project must 404 instead of listing "no keys", so a typo
// in `kuso shared-secret list` doesn't read as an empty project.
func TestListKeys_UnknownProjectIsNotFound(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRProjects: "KusoProjectList"})
	seedUnstructured(t, dyn, kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
	})
	s := New(&kube.Client{Clientset: kubefake.NewSimpleClientset(), Dynamic: dyn}, "kuso")

	keys, err := s.ListKeys(context.Background(), "alpha")
	if err != nil || len(keys) != 0 {
		t.Fatalf("existing project without secret: keys=%v err=%v (want [] nil)", keys, err)
	}
	if _, err := s.ListKeys(context.Background(), "nosuch"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: err=%v (want ErrNotFound)", err)
	}
}
