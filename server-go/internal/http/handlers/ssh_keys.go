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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/nodejoin"
)

// SSHKeysHandler exposes /api/ssh-keys.
type SSHKeysHandler struct {
	DB     *db.DB
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
		http.Error(w, "internal", http.StatusInternalServerError)
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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	id := randomID16()
	row := db.SSHKey{ID: id, Name: body.Name}
	if body.Generate {
		kp, err := nodejoin.GenerateEd25519("kuso@" + id)
		if err != nil {
			h.Logger.Error("generate ssh key", "err", err)
			http.Error(w, "key generation failed", http.StatusInternalServerError)
			return
		}
		row.PublicKey = kp.PublicKey
		row.PrivateKey = kp.PrivateKey
		row.Fingerprint = kp.Fingerprint
	} else {
		if body.PublicKey == "" || body.PrivateKey == "" {
			http.Error(w, "either generate=true or both publicKey + privateKey required", http.StatusBadRequest)
			return
		}
		fp, err := nodejoin.FingerprintOf(body.PublicKey)
		if err != nil {
			http.Error(w, "invalid public key: "+err.Error(), http.StatusBadRequest)
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
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *SSHKeysHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := h.ctx(r)
	defer cancel()
	if err := h.DB.DeleteSSHKey(ctx, chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("delete ssh key", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func randomID16() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
