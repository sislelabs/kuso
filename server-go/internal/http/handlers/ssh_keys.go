// SSH key library — POST/GET/DELETE under /api/ssh-keys. Powers the
// "Add node" flow: the operator either pastes an existing public key
// (we store both halves, surface the public for them to copy), or has
// kuso generate a fresh ed25519 keypair (server-side; private stays
// in SQLite). Each key is reusable across multiple node joins so the
// operator doesn't manage one private blob per server.

package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
	"kuso/server/internal/nodejoin"
)

// SSHKeysHandler exposes /api/ssh-keys.
// Audited: an SSH key is a credential that grants access to node-join
// and git remotes, so "who added/removed which key" must outlive the
// pod's slog buffer. Only the NAME and FINGERPRINT are ever recorded —
// never the public or private key material, since audit rows are
// readable by any holder of audit:read, which is a weaker permission
// than the settings:admin needed to mint the key.
type SSHKeysHandler struct {
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

func (h *SSHKeysHandler) Mount(r chi.Router) {
	r.Get("/api/ssh-keys", h.List)
	r.Post("/api/ssh-keys", h.Create)
	r.Delete("/api/ssh-keys/{id}", h.Delete)
}

func (h *SSHKeysHandler) ctx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *SSHKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := h.ctx(r)
	defer cancel()
	keys, err := h.DB.ListSSHKeys(ctx)
	if err != nil {
		h.Logger.Error("list ssh keys", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// Create accepts either {name, generate: true} (server generates a
// fresh ed25519 keypair) or {name, publicKey, privateKey} (operator
// pastes their own key). The response always includes the public
// half + fingerprint so the UI can show a copy-paste-ready
// authorized_keys line.
func (h *SSHKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Name       string `json:"name"`
		Generate   bool   `json:"generate"`
		PublicKey  string `json:"publicKey,omitempty"`
		PrivateKey string `json:"privateKey,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	id := randomID16()
	row := db.SSHKey{ID: id, Name: body.Name}
	if body.Generate {
		kp, err := nodejoin.GenerateEd25519("kuso@" + id)
		if err != nil {
			h.Logger.Error("generate ssh key", "err", err)
			writeErr(w, http.StatusInternalServerError, "key generation failed")
			return
		}
		row.PublicKey = kp.PublicKey
		row.PrivateKey = kp.PrivateKey
		row.Fingerprint = kp.Fingerprint
	} else {
		if body.PublicKey == "" || body.PrivateKey == "" {
			writeErr(w, http.StatusBadRequest, "either generate=true or both publicKey + privateKey required")
			return
		}
		fp, err := nodejoin.FingerprintOf(body.PublicKey)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid public key: "+err.Error())
			return
		}
		row.PublicKey = strings.TrimSpace(body.PublicKey)
		row.PrivateKey = body.PrivateKey
		row.Fingerprint = fp
	}

	ctx, cancel := h.ctx(r)
	defer cancel()
	if err := h.DB.CreateSSHKey(ctx, row); err != nil {
		h.Logger.Error("insert ssh key", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Fingerprint only — never row.PublicKey / row.PrivateKey.
	h.auditKey(ctx, "ssh-key.create", fmt.Sprintf(
		"created ssh key %q (id=%s, fingerprint=%s, generated=%t)",
		row.Name, id, row.Fingerprint, body.Generate))
	writeJSON(w, http.StatusCreated, row)
}

func (h *SSHKeysHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := h.ctx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	// Resolve the name/fingerprint BEFORE deleting so the audit row says
	// which key went away rather than just an opaque id. Best-effort: a
	// failed lookup must not block the delete.
	var name, fingerprint string
	if k, err := h.DB.GetSSHKey(ctx, id); err == nil && k != nil {
		name, fingerprint = k.Name, k.Fingerprint
	}
	if err := h.DB.DeleteSSHKey(ctx, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "SSH key not found")
			return
		}
		h.Logger.Error("delete ssh key", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	h.auditKey(ctx, "ssh-key.delete", fmt.Sprintf(
		"deleted ssh key %q (id=%s, fingerprint=%s)", name, id, fingerprint))
	w.WriteHeader(http.StatusNoContent)
}

// auditKey writes one ssh-key audit row. Severity "warn": adding or
// removing a credential is never routine.
func (h *SSHKeysHandler) auditKey(ctx context.Context, action, message string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "warn",
		Action:   action,
		Resource: "ssh-key",
		Message:  message,
	})
}

func randomID16() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
