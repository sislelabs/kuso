package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// TokensAdminHandler hosts the admin-scope token routes:
//
//	GET    /api/tokens
//	POST   /api/tokens/user/{userId}
//	DELETE /api/tokens/{id}
//
// The current-user routes (/api/tokens/my*) live on AdminHandler.
type TokensAdminHandler struct {
	DB     *db.DB
	Issuer *auth.Issuer
	Logger *slog.Logger
}

func (h *TokensAdminHandler) Mount(r chi.Router) {
	r.Get("/api/tokens", h.ListAll)
	r.Post("/api/tokens/user/{userId}", h.IssueForUser)
	r.Delete("/api/tokens/{id}", h.Delete)
}

func tokAdminCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *TokensAdminHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := tokAdminCtx(r)
	defer cancel()
	rows, err := h.DB.ListAllTokens(ctx)
	if err != nil {
		h.Logger.Error("list all tokens", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		out = append(out, map[string]any{
			"id":        t.ID,
			"name":      nullStr(t.Name),
			"createdAt": t.CreatedAt.UTC().Format(time.RFC3339),
			"expiresAt": t.ExpiresAt.UTC().Format(time.RFC3339),
			"isActive":  t.IsActive,
			"user": map[string]any{
				"id": t.UserID, "username": t.Username, "email": t.Email,
			},
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// IssueForUser mints a token JWT for an arbitrary user. Admin-only.
// Body shape mirrors the user-self path: {name, expiresAt}.
func (h *TokensAdminHandler) IssueForUser(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	// Empty / "never" / "null" → infinite token. DB row carries a
	// 100y sentinel; the JWT itself omits the exp claim.
	var expiresAt time.Time
	infinite := false
	if req.ExpiresAt == "" || req.ExpiresAt == "never" || req.ExpiresAt == "null" {
		infinite = true
		expiresAt = time.Now().UTC().AddDate(100, 0, 0)
	} else {
		var err error
		expiresAt, err = time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			http.Error(w, "expiresAt must be RFC3339 or empty for never-expires", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := tokAdminCtx(r)
	defer cancel()
	user, err := h.DB.FindUserByID(ctx, chi.URLParam(r, "userId"))
	if err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "user not found", http.StatusNotFound)
		default:
			h.Logger.Error("find user", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	roleName, _ := h.DB.UserRoleName(ctx, user.ID)
	if roleName == "" {
		roleName = "none"
	}
	groups, _ := h.DB.UserGroupNames(ctx, user.ID)
	if groups == nil {
		groups = []string{}
	}
	perms, _ := h.DB.UserPermissions(ctx, user.ID)
	if perms == nil {
		perms = []string{}
	}
	signExpiry := expiresAt
	if infinite {
		signExpiry = time.Time{}
	}
	jwt, err := h.Issuer.SignWithExpiry(auth.Claims{
		UserID: user.ID, Username: user.Username, Role: roleName,
		UserGroups: groups, Permissions: perms, Strategy: "token",
	}, signExpiry)
	if err != nil {
		h.Logger.Error("sign token", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	id, err := randomID()
	if err != nil {
		h.Logger.Error("token id", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	row := &db.Token{
		ID:        id,
		UserID:    user.ID,
		ExpiresAt: expiresAt,
		IsActive:  true,
		CreatedAt: time.Now().UTC(),
	}
	row.Name.Valid = true
	row.Name.String = req.Name
	if err := h.DB.CreateToken(ctx, row); err != nil {
		h.Logger.Error("create token", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": req.Name, "token": jwt, "expiresAt": expiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *TokensAdminHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := tokAdminCtx(r)
	defer cancel()
	if err := h.DB.DeleteToken(ctx, chi.URLParam(r, "id")); err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("delete token", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
