package addons

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func unsubFixture(t *testing.T) *dynamicfake.FakeDynamicClient {
	t.Helper()
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
	return dyn
}

// OPS-7: a failed service write left the service subscribed to the
// deleted addon with nobody told; re-adding the name re-mounted it.
func TestDelete_UnsubscribeFailureIsReported(t *testing.T) {
	t.Parallel()
	dyn := unsubFixture(t)
	dyn.PrependReactor("update", "kusoservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver unavailable")
	})
	s := &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, Namespace: "kuso"}

	err := s.Delete(context.Background(), "alpha", "db")
	if !errors.Is(err, ErrEnvRefresh) || !strings.Contains(err.Error(), "alpha-web") {
		t.Fatalf("Delete err = %v, want ErrEnvRefresh naming alpha-web", err)
	}
}

// SCL-13: the unsubscribe write is a conflict-retried read-modify-write,
// so a concurrent writer bumping the resourceVersion doesn't lose it.
func TestDelete_UnsubscribeRetriesConflict(t *testing.T) {
	t.Parallel()
	dyn := unsubFixture(t)
	var conflicts atomic.Int32
	dyn.PrependReactor("update", "kusoservices", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts.Add(1) == 1 {
			return true, nil, apierrors.NewConflict(kube.GVRServices.GroupResource(), "alpha-web", errors.New("rv changed"))
		}
		return false, nil, nil
	})
	s := &Service{Kube: &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset()}, Namespace: "kuso"}

	if err := s.Delete(context.Background(), "alpha", "db"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	svc, err := dyn.Resource(kube.GVRServices).Namespace("kuso").Get(context.Background(), "alpha-web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	subs, _, _ := unstructured.NestedStringSlice(svc.Object, "spec", "subscribedAddons")
	if !slices.Equal(subs, []string{"cache"}) {
		t.Fatalf("subscribedAddons = %v, want [cache]", subs)
	}
	if conflicts.Load() < 2 {
		t.Fatalf("update attempts = %d, want a retry after the conflict", conflicts.Load())
	}
}
