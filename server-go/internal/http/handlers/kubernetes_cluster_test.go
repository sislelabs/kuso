package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
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

// LOOP-09: the apiserver pages events in key (name) order, not time
// order, so one Limit:200 page of a busy namespace misses the newest.
func TestEvents_ReturnsNewestAcrossPages(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var all []corev1.Event
	for i := 0; i < 450; i++ {
		// Names sort oldest-first, like etcd's <object>.<hash> keys can.
		all = append(all, corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Name: fmt.Sprintf("ev-%04d", i), Namespace: "kuso"},
			LastTimestamp: metav1.NewTime(base.Add(time.Duration(i) * time.Minute)),
		})
	}
	cs := kubefake.NewClientset()
	cs.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		opts := a.(k8stesting.ListActionImpl).ListOptions
		start := 0
		if opts.Continue != "" {
			start, _ = strconv.Atoi(opts.Continue)
		}
		end := len(all)
		if opts.Limit > 0 && start+int(opts.Limit) < end {
			end = start + int(opts.Limit)
		}
		out := &corev1.EventList{Items: all[start:end]}
		if end < len(all) {
			out.Continue = strconv.Itoa(end)
		}
		return true, out, nil
	})
	h := &KubernetesHandler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	rr := httptest.NewRecorder()
	h.Events(rr, adminReq())
	var got []corev1.Event
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (status %d)", err, rr.Code)
	}
	if len(got) != 200 {
		t.Fatalf("got %d events, want 200", len(got))
	}
	if got[0].Name != "ev-0449" || got[199].Name != "ev-0250" {
		t.Errorf("window = %s..%s, want ev-0449..ev-0250 (newest 200)", got[0].Name, got[199].Name)
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
