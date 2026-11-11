package handlers

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/audit"
	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

// Backup encryption keys (one <addon>-backup-key Secret per addon) are the
// only way to read that addon's encrypted dumps, and they live in the
// cluster the dumps are meant to outlive. Export hands them to an admin
// (optionally sealed to an operator-held X25519 key); import puts them back
// on a rebuilt cluster before the addons are re-created, so the chart's
// lookup reuses them instead of minting fresh ones.

const maxBackupKeyBundle = 4 << 20

// ExportBackupKeys implements GET /api/admin/backup/keys[?recipient=<pub>].
func (h *BackupHandler) ExportBackupKeys(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, "backup keys unavailable: no kube client")
		return
	}
	rcpt, err := parseRecipient(r.URL.Query().Get("recipient"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "recipient: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	list, err := h.Kube.Clientset.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: backup.KeyRoleSelector()})
	if err != nil {
		h.Logger.Error("backup keys: list", "err", err)
		writeErr(w, http.StatusBadGateway, "list backup key secrets: "+err.Error())
		return
	}
	now := time.Now().UTC()
	bundle := apiv1.BackupKeyBundle{SchemaVersion: apiv1.BackupKeyBundleSchema, ExportedAt: now.Format(time.RFC3339), Keys: []apiv1.BackupKey{}}
	for _, s := range list.Items {
		key := string(s.Data[backup.KeySecretKey])
		if key == "" {
			continue
		}
		bundle.Keys = append(bundle.Keys, apiv1.BackupKey{
			Namespace: s.Namespace,
			Secret:    s.Name,
			Project:   s.Labels[kube.LabelProject],
			Addon:     s.Labels["kuso.sislelabs.com/addon"],
			AddonKind: s.Labels["kuso.sislelabs.com/addon-kind"],
			KeyID:     backup.KeyID(key),
			Key:       key,
		})
	}
	sort.Slice(bundle.Keys, func(i, j int) bool {
		a, b := bundle.Keys[i], bundle.Keys[j]
		return a.Namespace+"/"+a.Secret < b.Namespace+"/"+b.Secret
	})

	resp := apiv1.BackupKeyExportResponse{Count: len(bundle.Keys)}
	if rcpt != nil {
		raw, err := json.Marshal(bundle)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal")
			return
		}
		sealed, err := apiv1.SealBackupKeys(rcpt, raw)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "seal bundle: "+err.Error())
			return
		}
		resp.Sealed = base64.StdEncoding.EncodeToString(sealed)
	} else {
		resp.Bundle = &bundle
	}

	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "critical",
		Action:   "backup.keys_export",
		Resource: "secret",
		Message:  fmt.Sprintf("exported %d backup encryption keys (sealed=%t)", len(bundle.Keys), rcpt != nil),
	})

	// Stamped only once the response is ready, so health stops warning
	// about a key exactly when someone holds a copy of it. Forbidden or
	// failed stamps don't fail the export: the caller has the keys.
	stamp := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, backup.KeyExportedAtAnnotation, now.Format(time.RFC3339)))
	for _, k := range bundle.Keys {
		if _, err := h.Kube.Clientset.CoreV1().Secrets(k.Namespace).Patch(ctx, k.Secret, types.MergePatchType, stamp, metav1.PatchOptions{}); err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("%s/%s: exported, but could not record the export (health will keep warning): %v", k.Namespace, k.Secret, err))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}

// parseRecipient returns nil for an empty recipient (plaintext export).
func parseRecipient(s string) (*ecdh.PublicKey, error) {
	if s == "" {
		return nil, nil
	}
	return apiv1.ParseBackupKeyRecipient(s)
}

// ImportBackupKeys implements POST /api/admin/backup/keys. It only creates
// missing key Secrets; one holding a different key is a conflict and is
// never overwritten (its dumps would become unreadable).
func (h *BackupHandler) ImportBackupKeys(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, "backup keys unavailable: no kube client")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBackupKeyBundle+1))
	if err != nil || len(raw) > maxBackupKeyBundle {
		writeErr(w, http.StatusBadRequest, "read bundle: too large or unreadable")
		return
	}
	var bundle apiv1.BackupKeyBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		writeErr(w, http.StatusBadRequest, "decode bundle: "+err.Error())
		return
	}
	if bundle.SchemaVersion > apiv1.BackupKeyBundleSchema {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("bundle schemaVersion %d is newer than this server understands (%d)", bundle.SchemaVersion, apiv1.BackupKeyBundleSchema))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	out := apiv1.BackupKeyImportResponse{Results: []apiv1.BackupKeyImportResult{}}
	conflict := false
	created := 0
	for _, k := range bundle.Keys {
		res := h.importBackupKey(ctx, k)
		switch res.Status {
		case "conflict":
			conflict = true
		case "created":
			created++
		}
		out.Results = append(out.Results, res)
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "critical",
		Action:   "backup.keys_import",
		Resource: "secret",
		Message:  fmt.Sprintf("imported backup encryption keys: %d created of %d in bundle (conflict=%t)", created, len(bundle.Keys), conflict),
	})
	status := http.StatusOK
	if conflict {
		status = http.StatusConflict
	}
	writeJSON(w, status, out)
}

func (h *BackupHandler) importBackupKey(ctx context.Context, k apiv1.BackupKey) apiv1.BackupKeyImportResult {
	res := apiv1.BackupKeyImportResult{Namespace: k.Namespace, Secret: k.Secret}
	release, ok := backup.ReleaseForKeySecret(k.Secret)
	if !ok || k.Namespace == "" || k.Key == "" {
		res.Status, res.Detail = "error", "not a backup key entry (secret must be <addon>-backup-key with a namespace and key)"
		return res
	}
	if k.KeyID != "" && backup.KeyID(k.Key) != k.KeyID {
		res.Status, res.Detail = "error", "key does not match its keyId; the bundle is damaged"
		return res
	}
	secrets := h.Kube.Clientset.CoreV1().Secrets(k.Namespace)
	if cur, err := secrets.Get(ctx, k.Secret, metav1.GetOptions{}); err == nil {
		if string(cur.Data[backup.KeySecretKey]) == k.Key {
			res.Status = "unchanged"
			return res
		}
		res.Status = "conflict"
		res.Detail = fmt.Sprintf("Secret already holds a different key (keyId %s, bundle has %s); left untouched. Dumps written since this cluster was rebuilt use the live key; restore older ones by exporting the live key first, then replacing it by hand",
			backup.KeyID(string(cur.Data[backup.KeySecretKey])), backup.KeyID(k.Key))
		return res
	} else if !apierrors.IsNotFound(err) {
		res.Status, res.Detail = "error", "read existing Secret: "+err.Error()
		return res
	}
	labels := map[string]string{
		backup.KeyRoleLabel:            backup.KeyRoleValue,
		"app.kubernetes.io/name":       "kusoaddon",
		"app.kubernetes.io/instance":   release,
		"app.kubernetes.io/managed-by": "Helm",
		"kuso.sislelabs.com/addon":     release,
	}
	if k.Project != "" {
		labels[kube.LabelProject] = k.Project
	}
	if k.AddonKind != "" {
		labels["kuso.sislelabs.com/addon-kind"] = k.AddonKind
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      k.Secret,
			Namespace: k.Namespace,
			Labels:    labels,
			// Helm ownership lets the addon's release adopt the Secret
			// (otherwise its upgrade fails "exists and cannot be imported");
			// the chart's lookup then keeps this key instead of minting one.
			Annotations: map[string]string{
				"helm.sh/resource-policy":        "keep",
				"meta.helm.sh/release-name":      release,
				"meta.helm.sh/release-namespace": k.Namespace,
				backup.KeyExportedAtAnnotation:   time.Now().UTC().Format(time.RFC3339),
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{backup.KeySecretKey: []byte(k.Key)},
	}
	if _, err := secrets.Create(ctx, sec, metav1.CreateOptions{}); err != nil {
		res.Status, res.Detail = "error", "create Secret: "+err.Error()
		if apierrors.IsNotFound(err) {
			res.Detail += " (create the project first so its namespace exists)"
		}
		return res
	}
	res.Status = "created"
	return res
}
