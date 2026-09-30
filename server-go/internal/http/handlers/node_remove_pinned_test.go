package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// localPVC is a claim the scheduler bound to node. kuso-server may not list
// PersistentVolumes (cluster-scoped, outside its RBAC), so the node comes
// from the claim's selected-node annotation.
func localPVC(ns, name, node string, labels map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name, Labels: labels,
			Annotations: map[string]string{selectedNodeAnnotation: node},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
		},
	}
}

func TestPinnedVolumesOnNode(t *testing.T) {
	t.Parallel()
	pvcs := []corev1.PersistentVolumeClaim{
		*localPVC("kuso", "data-alpha-pg-0", "worker-1", map[string]string{
			"app.kubernetes.io/name": "kusoaddon", "app.kubernetes.io/instance": "alpha-pg",
		}),
		*localPVC("kuso", "alpha-web-production-uploads", "worker-1", map[string]string{
			"kuso.sislelabs.com/project": "alpha", "kuso.sislelabs.com/service": "alpha-web",
			"kuso.sislelabs.com/volume": "uploads",
		}),
		*localPVC("kuso", "data-alpha-redis-0", "worker-2", nil),
		// Never scheduled: no node, nothing to strand.
		{ObjectMeta: metav1.ObjectMeta{Namespace: "kuso", Name: "pending-claim"}},
	}

	got := pinnedVolumesOnNode(pvcs, "worker-1")
	if len(got) != 2 {
		t.Fatalf("got %d pinned, want 2: %+v", len(got), got)
	}
	if got[0].PVC != "alpha-web-production-uploads" || got[0].Kind != "volume" || got[0].Owner != "alpha-web/uploads" {
		t.Errorf("volume entry = %+v", got[0])
	}
	if got[1].PVC != "data-alpha-pg-0" || got[1].Kind != "addon" || got[1].Owner != "alpha-pg" || got[1].Size != "5Gi" {
		t.Errorf("addon entry = %+v", got[1])
	}
}

func removeNodeRequest(t *testing.T, cs *kubefake.Clientset, query string) *httptest.ResponseRecorder {
	t.Helper()
	h := &KubernetesHandler{Kube: &kube.Client{Clientset: cs}, Logger: slog.Default()}
	r := chi.NewRouter()
	r.Post("/api/kubernetes/nodes/{name}/remove", h.RemoveNode)
	ctx := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	req := httptest.NewRequest(http.MethodPost, "/api/kubernetes/nodes/worker-1/remove"+query, strings.NewReader(`{}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRemoveNode_RefusesWhenDataPinned(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		localPVC("kuso", "data-alpha-pg-0", "worker-1", nil),
	)
	rec := removeNodeRequest(t, cs, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data-alpha-pg-0") {
		t.Errorf("body should list the pinned PVC: %s", rec.Body.String())
	}
	if _, err := cs.CoreV1().Nodes().Get(context.Background(), "worker-1", metav1.GetOptions{}); err != nil {
		t.Errorf("node was removed despite the refusal: %v", err)
	}
}

func TestRemoveNode_ForceRemovesDespitePinnedData(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		localPVC("kuso", "data-alpha-pg-0", "worker-1", nil),
	)
	rec := removeNodeRequest(t, cs, "?force=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}
