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

func localPV(name, node, ns, pvc string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
			ClaimRef: &corev1.ObjectReference{Namespace: ns, Name: pvc},
			NodeAffinity: &corev1.VolumeNodeAffinity{Required: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpIn, Values: []string{node},
				}}}},
			}},
		},
	}
}

func TestPinnedVolumesOnNode(t *testing.T) {
	t.Parallel()
	pvs := []corev1.PersistentVolume{
		*localPV("pv-a", "worker-1", "kuso", "data-alpha-pg-0"),
		*localPV("pv-b", "worker-1", "kuso", "alpha-web-production-uploads"),
		*localPV("pv-c", "worker-2", "kuso", "data-alpha-redis-0"),
		// Network storage: no node affinity, survives node removal.
		{ObjectMeta: metav1.ObjectMeta{Name: "pv-d"}, Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{Namespace: "kuso", Name: "nfs-claim"},
		}},
	}
	pvcs := map[string]*corev1.PersistentVolumeClaim{
		"kuso/data-alpha-pg-0": {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"app.kubernetes.io/name": "kusoaddon", "app.kubernetes.io/instance": "alpha-pg",
		}}},
		"kuso/alpha-web-production-uploads": {ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"kuso.sislelabs.com/project": "alpha", "kuso.sislelabs.com/service": "alpha-web",
			"kuso.sislelabs.com/volume": "uploads",
		}}},
	}

	got := pinnedVolumesOnNode(pvs, pvcs, "worker-1")
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
		localPV("pv-a", "worker-1", "kuso", "data-alpha-pg-0"),
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
		localPV("pv-a", "worker-1", "kuso", "data-alpha-pg-0"),
	)
	rec := removeNodeRequest(t, cs, "?force=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}
