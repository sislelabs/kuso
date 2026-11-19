package status

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// release.json pins images as repo:tag@sha256:…; the operator version
// must still be the tag, not the digest tail.
func TestFetchOperatorVersion_DigestPinnedImage(t *testing.T) {
	t.Parallel()
	for img, want := range map[string]string{
		"ghcr.io/sislelabs/kuso-operator:v0.32.4":                 "v0.32.4",
		"ghcr.io/sislelabs/kuso-operator:v0.32.4@sha256:0123abcd": "v0.32.4",
	} {
		cs := fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-operator-controller-manager-x", Namespace: "kuso-operator-system"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: img}}},
		})
		s := &Service{Kube: &kube.Client{Clientset: cs}}
		if got := s.fetchOperatorVersion(context.Background()); got != want {
			t.Errorf("%s: got %q, want %q", img, got, want)
		}
	}
}

// A discovery blip must not replace a good cached kube version.
func TestVersions_DiscoveryErrorKeepsCachedKubeVersion(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.31.2"}
	s := New(&kube.Client{Clientset: cs}, time.Hour)
	if kv, _ := s.Versions(context.Background()); kv != "v1.31.2" {
		t.Fatalf("first load = %q", kv)
	}
	cs.PrependReactor("get", "version", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("discovery blip")
	})
	s.mu.Lock()
	s.loadedAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()
	if kv, _ := s.Versions(context.Background()); kv != "v1.31.2" {
		t.Fatalf("after a discovery error = %q, want the cached v1.31.2", kv)
	}
}

// Concurrent callers on a stale cache trigger one refresh, not one each.
func TestVersions_StaleCacheRefreshesOnce(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	var calls atomic.Int32
	release := make(chan struct{})
	cs.PrependReactor("get", "version", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		<-release
		return false, nil, nil
	})
	s := New(&kube.Client{Clientset: cs}, time.Hour)
	s.kubeVer, s.loadedAt = "v1.30.0", time.Now().Add(-2*time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Versions(context.Background())
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery called %d times, want 1", n)
	}
}
