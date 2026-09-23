package handlers_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/kube"
)

// Custom-namespace projects run their backup CronJobs against a local copy
// of kuso-backup-s3. A rotated key must reach that copy when the settings
// are saved, not at the next server restart.
func TestPutBackupSettings_RefreshesCustomNamespaceCopies(t *testing.T) {
	secret := func(ns, key string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: ns},
			Data: map[string][]byte{
				"bucket": []byte("b"), "endpoint": []byte("https://s3.example.com"),
				"accessKeyId": []byte("AK"), "secretAccessKey": []byte(key),
			},
		}
	}
	managed := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: name, Labels: map[string]string{kube.ManagedByLabel: kube.ManagedByValue},
		}}
	}
	cs := k8sfake.NewSimpleClientset(
		managed("kuso"), managed("kuso-koreni"),
		secret("kuso", "old-key"), secret("kuso-koreni", "old-key"),
	)
	h := &httphandlers.BackupsHandler{
		Kube:      &kube.Client{Clientset: cs},
		Namespace: "kuso",
		Logger:    slog.New(slog.DiscardHandler),
	}
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	h.Mount(r)

	body := `{"bucket":"b","endpoint":"https://s3.example.com","accessKeyId":"AK","secretAccessKey":"new-key"}`
	req := httptest.NewRequest(http.MethodPut, "/api/admin/backup-settings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	got, err := cs.CoreV1().Secrets("kuso-koreni").Get(context.Background(), "kuso-backup-s3", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if k := string(got.Data["secretAccessKey"]); k != "new-key" {
		t.Fatalf("kuso-koreni copy still has secretAccessKey=%q after rotation", k)
	}
}
