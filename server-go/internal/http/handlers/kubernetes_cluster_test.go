package handlers

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func adminReq() *http.Request {
	return reqWithClaims(&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
}

func resetClusterCache() {
	clusterCache.Lock()
	clusterCache.entries = map[string]clusterCacheEntry{}
	clusterCache.Unlock()
}

// A cluster whose kuso-server ClusterRole predates the storageclasses grant
// must get an actionable answer, not a bare 500 "internal".
func TestStorageClasses_RBACForbidden(t *testing.T) {
	resetClusterCache()
	t.Cleanup(resetClusterCache)
	cs := kubefake.NewClientset()
	cs.PrependReactor("list", "storageclasses", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "storage.k8s.io", Resource: "storageclasses"}, "", nil)
	})
	h := &KubernetesHandler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	rr := httptest.NewRecorder()
	h.StorageClasses(rr, adminReq())

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "storageclasses") {
		t.Errorf("body should name the missing grant: %s", rr.Body.String())
	}
}

func TestStorageClasses_Lists(t *testing.T) {
	resetClusterCache()
	t.Cleanup(resetClusterCache)
	cs := kubefake.NewClientset(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local-path"}})
	h := &KubernetesHandler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	rr := httptest.NewRecorder()
	h.StorageClasses(rr, adminReq())

	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "local-path") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
