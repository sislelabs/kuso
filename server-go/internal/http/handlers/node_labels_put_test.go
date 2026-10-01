package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// An unknown node or an invalid label value came back as a bare 500.
func TestPutNodeLabels_ErrorMapping(t *testing.T) {
	t.Parallel()
	h := &KubernetesHandler{
		Kube:   &kube.Client{Clientset: fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	put := func(node, body string) int {
		r := httptest.NewRequest(http.MethodPut, "/api/kubernetes/nodes/"+node+"/labels", strings.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", node)
		ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
		ctx = auth.WithClaimsForTest(ctx, &auth.Claims{Permissions: []string{string(auth.PermSettingsAdmin)}})
		rr := httptest.NewRecorder()
		h.PutNodeLabels(rr, r.WithContext(ctx))
		return rr.Code
	}
	if got := put("nope", `{"labels":{"gpu":"a100"}}`); got != http.StatusNotFound {
		t.Errorf("unknown node: %d, want 404", got)
	}
	if got := put("n1", `{"labels":{"gpu":"not a valid value!"}}`); got != http.StatusBadRequest {
		t.Errorf("bad value: %d, want 400", got)
	}
	if got := put("n1", `{"labels":{"gpu":"a100"}}`); got != http.StatusNoContent {
		t.Errorf("good label: %d, want 204", got)
	}
}
