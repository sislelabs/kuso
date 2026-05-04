package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// UsersHandler hosts the full /api/users CRUD that the admin pages use.
// Read-only listing + profile already live on AdminHandler; mutations
// live here so the file's blast radius stays small.
type UsersHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the user mutation routes onto the bearer-protected
// router. The READ routes (/api/users, /api/users/count,
// /api/users/profile) stay on AdminHandler.
func (h *UsersHandler) Mount(r chi.Router) {
	r.Post("/api/users", h.Create)
	r.Get("/api/users/username/{username}", h.GetByUsername)
	r.Get("/api/users/id/{id}", h.GetByID)
	r.Put("/api/users/id/{id}", h.Update)
	r.Delete("/api/users/id/{id}", h.Delete)
	r.Put("/api/users/id/{id}/password", h.UpdatePassword)
	r.Put("/api/users/profile", h.UpdateProfile)
	r.Put("/api/users/profile/password", h.UpdateMyPassword)
	r.Post("/api/users/profile/avatar", h.UpdateMyAvatar)
}

func usersCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// createUserRequest mirrors the TS payload shape.
type createUserRequest struct {
	Username  string `json:"username"`
	Email     string `json:"email"`
	Password  string `json:"password"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	RoleID    string `json:"roleId"`
	IsActive  *bool  `json:"isActive"`
}

func (h *UsersHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		http.Error(w, "username, email, password required", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(req.Password, 0)
	if err != nil {
		h.fail(w, "hash password", err)
		return
	}
	id := randomID()
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.CreateUser(ctx, db.CreateUserInput{
		ID: id, Username: req.Username, Email: req.Email, FirstName: req.FirstName, LastName: req.LastName,
		PasswordHash: hash, RoleID: req.RoleID, IsActive: active,
	}); err != nil {
		h.fail(w, "create user", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "username": req.Username, "email": req.Email,
		"firstName": req.FirstName, "lastName": req.LastName, "isActive": active, "roleId": req.RoleID,
	})
}

func (h *UsersHandler) GetByUsername(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	u, err := h.DB.FindUserByUsername(ctx, chi.URLParam(r, "username"))
	if err != nil {
		h.fail(w, "find user", err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse(u))
}

func (h *UsersHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	u, err := h.DB.FindUserByID(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find user", err)
		return
	}
	writeJSON(w, http.StatusOK, userResponse(u))
}

// updateUserRequest is partial — only the supplied fields land in the
// UPDATE statement.
type updateUserRequest struct {
	FirstName *string `json:"firstName,omitempty"`
	LastName  *string `json:"lastName,omitempty"`
	Email     *string `json:"email,omitempty"`
	RoleID    *string `json:"roleId,omitempty"`
	IsActive  *bool   `json:"isActive,omitempty"`
}

func (h *UsersHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.UpdateUser(ctx, chi.URLParam(r, "id"), db.UpdateUserInput{
		FirstName: req.FirstName, LastName: req.LastName, Email: req.Email, RoleID: req.RoleID, IsActive: req.IsActive,
	}); err != nil {
		h.fail(w, "update user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UsersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.DeleteUser(ctx, chi.URLParam(r, "id")); err != nil {
		h.fail(w, "delete user", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdatePassword (admin path) skips the current-password check.
func (h *UsersHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	if !requireUserWrite(w, r) {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == "" {
		http.Error(w, "password required", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(body.Password, 0)
	if err != nil {
		h.fail(w, "hash", err)
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.UpdateUserPassword(ctx, chi.URLParam(r, "id"), hash); err != nil {
		h.fail(w, "update password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateProfile lets the current user edit their own first/last/email.
func (h *UsersHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Profile edits MUST NOT touch role or active flag — those are
	// admin-only fields. Strip them defensively so a forged body can't
	// privilege-escalate via this route.
	req.RoleID = nil
	req.IsActive = nil
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.UpdateUser(ctx, claims.UserID, db.UpdateUserInput{
		FirstName: req.FirstName, LastName: req.LastName, Email: req.Email,
	}); err != nil {
		h.fail(w, "update profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateMyPassword requires the current password to verify before
// overwriting.
func (h *UsersHandler) UpdateMyPassword(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		body.CurrentPassword == "" || len(body.NewPassword) < 8 {
		http.Error(w, "currentPassword and newPassword (>=8 chars) required", http.StatusBadRequest)
		return
	}
	ctx, cancel := usersCtx(r)
	defer cancel()
	u, err := h.DB.FindUserByID(ctx, claims.UserID)
	if err != nil {
		h.fail(w, "load user", err)
		return
	}
	if err := auth.VerifyPassword(u.Password, body.CurrentPassword, ""); err != nil {
		http.Error(w, "current password incorrect", http.StatusForbidden)
		return
	}
	hash, err := auth.HashPassword(body.NewPassword, 0)
	if err != nil {
		h.fail(w, "hash", err)
		return
	}
	if err := h.DB.UpdateUserPassword(ctx, claims.UserID, hash); err != nil {
		h.fail(w, "update", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateMyAvatar accepts a multipart "avatar" file, base64-encodes it,
// and stores it in User.image as a data URL. Cap at 1 MiB so a stray
// 4K JPEG can't fill the SQLite file.
func (h *UsersHandler) UpdateMyAvatar(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	file, hdr, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "missing avatar field", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		h.fail(w, "read avatar", err)
		return
	}
	mime := hdr.Header.Get("Content-Type")
	if mime == "" {
		mime = "image/png"
	}
	dataURL := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	ctx, cancel := usersCtx(r)
	defer cancel()
	if err := h.DB.UpdateUser(ctx, claims.UserID, db.UpdateUserInput{Image: &dataURL}); err != nil {
		h.fail(w, "update avatar", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// userResponse formats a *db.User into the JSON shape the UI expects.
func userResponse(u *db.User) map[string]any {
	return map[string]any{
		"id":        u.ID,
		"username":  u.Username,
		"email":     u.Email,
		"firstName": nullStr(u.FirstName),
		"lastName":  nullStr(u.LastName),
		"isActive":  u.IsActive,
		"roleId":    nullStr(u.RoleID),
		"image":     nullStr(u.Image),
		"provider":  nullStr(u.Provider),
	}
}

func (h *UsersHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		h.Logger.Error("users handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
