package handlers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func adminRestoreRequest(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/restore", bytes.NewReader(body))
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	return req.WithContext(auth.WithClaimsForTest(req.Context(), c))
}

// A real control-plane dump is well over the 900 KiB a Secret can carry.
// It used to be a flat 413; now it goes through backup storage, and only
// says 413 when that storage isn't configured.
func TestRestoreUpload_LargeDumpNeedsBackupStorage(t *testing.T) {
	h := NewBackupHandler(nil, &kube.Client{Clientset: kubefake.NewSimpleClientset()}, "kuso", slog.Default())
	rr := httptest.NewRecorder()
	h.Upload(rr, adminRestoreRequest(bytes.Repeat([]byte{0x1f}, secretRestoreMax+10)))
	if rr.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rr.Body.String(), "backup settings") {
		t.Fatalf("status=%d body=%s; want 413 naming backup settings", rr.Code, rr.Body.String())
	}
}

func TestRestoreUpload_SmallDumpKeepsSecretPath(t *testing.T) {
	cs := kubefake.NewSimpleClientset()
	h := NewBackupHandler(nil, &kube.Client{Clientset: cs}, "kuso", slog.Default())
	rr := httptest.NewRecorder()
	h.Upload(rr, adminRestoreRequest(bytes.Repeat([]byte{0x1f}, 4096)))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s; want 202", rr.Code, rr.Body.String())
	}
	secs, _ := cs.CoreV1().Secrets("kuso").List(context.Background(), metav1.ListOptions{})
	if len(secs.Items) != 1 {
		t.Fatalf("want the dump in one Secret, got %d", len(secs.Items))
	}
}

func TestS3RestoreJob_VerifiesBeforeApplying(t *testing.T) {
	h := &BackupHandler{Namespace: "kuso"}
	job := h.s3RestoreJob("kuso-restore-x", restoreStagePrefix+"x.sql.gz", "abc")
	if job.Annotations[restoreKeyAnnotation] != restoreStagePrefix+"x.sql.gz" {
		t.Fatalf("staged key not recorded for cleanup: %v", job.Annotations)
	}
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	check := strings.Index(script, `"${EXPECT_SHA256}"`)
	apply := strings.Index(script, `psql "$DSN"`)
	if check < 0 || apply < 0 || check > apply {
		t.Fatalf("checksum check (at %d) must precede psql (at %d)", check, apply)
	}
	env := map[string]bool{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = true
	}
	for _, k := range []string{"KEY", "EXPECT_SHA256", "DSN", "BUCKET", "S3_ENDPOINT", "AWS_ACCESS_KEY_ID"} {
		if !env[k] {
			t.Errorf("job env missing %s", k)
		}
	}
}
