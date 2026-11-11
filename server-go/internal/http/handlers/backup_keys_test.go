package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/backup"
	"kuso/server/internal/backuphealth"
	"kuso/server/internal/kube"
)

func keySecret(ns, name, key string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			backup.KeyRoleLabel: backup.KeyRoleValue, kube.LabelProject: "p", "kuso.sislelabs.com/addon": name[:len(name)-len("-backup-key")],
		}},
		Data: map[string][]byte{backup.KeySecretKey: []byte(key)},
	}
}

func keysAdminReq(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	return req.WithContext(auth.WithClaimsForTest(req.Context(), c))
}

// DB-2: every backup key must be exportable, an export must clear the
// health warning, and a re-import on a rebuilt cluster must recreate the
// Secret so the chart adopts it rather than minting a new key.
// (Fake clientset: RBAC is not exercised here.)
func TestBackupKeys_ExportStampsAndImportRestores(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewSimpleClientset(
		keySecret("kuso", "kuso-instance-pg-backup-key", "k-inst"),
		keySecret("kuso-p", "p-db-backup-key", "k-db"),
	)
	kc := &kube.Client{Clientset: cs}
	h := NewBackupHandler(nil, kc, "kuso", slog.Default())

	if left, ok := backuphealth.UnexportedBackupKeys(ctx, kc); !ok || len(left) != 2 {
		t.Fatalf("before export: unexported=%v complete=%v, want both keys", left, ok)
	}

	rr := httptest.NewRecorder()
	h.ExportBackupKeys(rr, keysAdminReq(http.MethodGet, "/api/admin/backup/keys", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", rr.Code, rr.Body)
	}
	var exp apiv1.BackupKeyExportResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &exp); err != nil || exp.Bundle == nil || exp.Count != 2 {
		t.Fatalf("export body: %v %s", err, rr.Body)
	}
	for _, k := range exp.Bundle.Keys {
		if k.KeyID != backup.KeyID(k.Key) {
			t.Errorf("%s: keyId %s does not fingerprint its key", k.Secret, k.KeyID)
		}
	}
	if left, _ := backuphealth.UnexportedBackupKeys(ctx, kc); len(left) != 0 {
		t.Fatalf("after export still unexported: %v", left)
	}

	// Sealed export: only decryptable with the operator's identity.
	priv, pub, _ := apiv1.GenerateBackupKeyIdentity()
	rr = httptest.NewRecorder()
	h.ExportBackupKeys(rr, keysAdminReq(http.MethodGet, "/api/admin/backup/keys?recipient="+url.QueryEscape(pub), nil))
	var sealedResp apiv1.BackupKeyExportResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &sealedResp)
	if sealedResp.Bundle != nil || bytes.Contains(rr.Body.Bytes(), []byte("k-db")) {
		t.Fatalf("sealed export leaked plaintext: %s", rr.Body)
	}
	sealed, _ := base64.StdEncoding.DecodeString(sealedResp.Sealed)
	id, _ := apiv1.ParseBackupKeyIdentity(priv)
	if pt, err := apiv1.OpenBackupKeys(id, sealed); err != nil || !bytes.Contains(pt, []byte("k-db")) {
		t.Fatalf("sealed export does not open: %v", err)
	}

	// Rebuilt cluster: the per-project key is gone, the instance one was
	// re-minted with a different value.
	_ = cs.CoreV1().Secrets("kuso-p").Delete(ctx, "p-db-backup-key", metav1.DeleteOptions{})
	inst, _ := cs.CoreV1().Secrets("kuso").Get(ctx, "kuso-instance-pg-backup-key", metav1.GetOptions{})
	inst.Data[backup.KeySecretKey] = []byte("k-new")
	_, _ = cs.CoreV1().Secrets("kuso").Update(ctx, inst, metav1.UpdateOptions{})

	raw, _ := json.Marshal(exp.Bundle)
	rr = httptest.NewRecorder()
	h.ImportBackupKeys(rr, keysAdminReq(http.MethodPost, "/api/admin/backup/keys", raw))
	if rr.Code != http.StatusConflict {
		t.Fatalf("import status=%d, want 409 for the re-minted key; body=%s", rr.Code, rr.Body)
	}
	got, err := cs.CoreV1().Secrets("kuso-p").Get(ctx, "p-db-backup-key", metav1.GetOptions{})
	if err != nil || string(got.Data[backup.KeySecretKey]) != "k-db" {
		t.Fatalf("missing key not recreated: %v", err)
	}
	if got.Annotations["meta.helm.sh/release-name"] != "p-db" || got.Annotations["helm.sh/resource-policy"] != "keep" ||
		got.Labels["app.kubernetes.io/managed-by"] != "Helm" {
		t.Errorf("recreated key lacks helm ownership, the addon's upgrade would refuse it: %v %v", got.Labels, got.Annotations)
	}
	inst, _ = cs.CoreV1().Secrets("kuso").Get(ctx, "kuso-instance-pg-backup-key", metav1.GetOptions{})
	if string(inst.Data[backup.KeySecretKey]) != "k-new" {
		t.Error("import overwrote a Secret holding a different key")
	}

	// Import refuses to write anything that is not a backup key.
	bad, _ := json.Marshal(apiv1.BackupKeyBundle{SchemaVersion: 1, Keys: []apiv1.BackupKey{{Namespace: "kuso", Secret: "kuso-server-secrets", Key: "x"}}})
	rr = httptest.NewRecorder()
	h.ImportBackupKeys(rr, keysAdminReq(http.MethodPost, "/api/admin/backup/keys", bad))
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "kuso-server-secrets", metav1.GetOptions{}); err == nil {
		t.Fatal("import wrote a non-key Secret")
	}
}

func TestBackupKeys_RequireAdmin(t *testing.T) {
	h := NewBackupHandler(nil, &kube.Client{Clientset: kubefake.NewSimpleClientset()}, "kuso", slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup/keys", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: "u2"}))
	rr := httptest.NewRecorder()
	h.ExportBackupKeys(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin export status=%d", rr.Code)
	}
}
