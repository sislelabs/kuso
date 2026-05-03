package handlers

// Invitation links — admin mints a token, shares the URL, the
// invitee redeems it through GH OAuth or local signup.
//
// Routes:
//   POST   /api/invites             — admin: mint a fresh invite
//   GET    /api/invites             — admin: list (newest-first)
//   DELETE /api/invites/{id}        — admin: revoke (soft) or delete
//   GET    /api/invites/lookup/{token} — public: read-only summary
//                                        for the signup page
//   POST   /api/invites/redeem      — public: create a local account
//                                        from {token, username, password}
//   POST   /api/invites/redeem/oauth/start — public: bind a token to
//                                             the GH OAuth state and
//                                             redirect (server-rendered
//                                             <form> on the signup page)
//
// The OAuth flow piggybacks on the existing /api/auth/github callback
// — we stash the invite ID in a short-lived cookie that the callback
// inspects and consumes.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// InvitesHandler hosts the invite endpoints. The Issuer is reused
// from the auth handler so redemption can mint a JWT inline.
type InvitesHandler struct {
	DB     *db.DB
	Issuer *auth.Issuer
	Logger *slog.Logger
}

func (h *InvitesHandler) Mount(r chi.Router) {
	// Bearer-protected admin routes. Mount() is called inside the
	// authed group in router.go, so anything mounted here gets the
	// JWT middleware automatically.
	r.Post("/api/invites", h.Create)
	r.Get("/api/invites", h.List)
	r.Delete("/api/invites/{id}", h.Revoke)
}

// MountPublic wires the routes that DON'T require an auth token —
// the invitee hasn't created an account yet, so we can't bearer-gate
// these. Token entropy is the security boundary instead.
func (h *InvitesHandler) MountPublic(r chi.Router) {
	r.Get("/api/invites/lookup/{token}", h.Lookup)
	r.Post("/api/invites/redeem", h.RedeemLocal)
	r.Get("/api/invites/redeem/oauth/start", h.RedeemOAuthStart)
}

func invCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// ---------- Create ----------

type createInviteRequest struct {
	GroupID      string `json:"groupId"`      // optional; empty → no group
	InstanceRole string `json:"instanceRole"` // optional; empty → use group default
	ExpiresIn    string `json:"expiresIn"`    // duration, e.g. "168h" (7d). Empty = never.
	MaxUses      int    `json:"maxUses"`      // default 1
	Note         string `json:"note"`
}

type createInviteResponse struct {
	Invite db.Invite `json:"invite"`
	URL    string    `json:"url"`
}

// Create mints a fresh invite. user:write gates the route at the
// router; we still cross-check here so a buggy router config doesn't
// silently grant unauth'd minting.
func (h *InvitesHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !auth.Has(claims.Permissions, auth.PermUserWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.MaxUses < 0 {
		http.Error(w, "maxUses must be >= 0", http.StatusBadRequest)
		return
	}
	if req.MaxUses == 0 {
		req.MaxUses = 1
	}
	// Multi-use links MUST have an expiry. An "unlimited + never
	// expires" link is a public registration page in disguise — gate
	// it explicitly so an admin doesn't trip into open signup.
	if req.MaxUses > 1 && req.ExpiresIn == "" {
		http.Error(w, "multi-use invites require expiresIn", http.StatusBadRequest)
		return
	}
	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil || d <= 0 {
			http.Error(w, "expiresIn: invalid duration", http.StatusBadRequest)
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	ctx, cancel := invCtx(r)
	defer cancel()

	// Validate the group exists if specified — surface a clear 400
	// instead of a foreign-key violation later.
	var groupID *string
	if req.GroupID != "" {
		if _, err := h.DB.GetGroup(ctx, req.GroupID); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				http.Error(w, "group not found", http.StatusBadRequest)
				return
			}
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		groupID = &req.GroupID
	}

	// Validate the instance role if specified — must be a known
	// constant. Anything else would silently grant unexpected
	// permissions on redemption.
	var instanceRole *string
	if req.InstanceRole != "" {
		switch req.InstanceRole {
		case "admin", "member", "viewer", "billing", "pending":
			ir := req.InstanceRole
			instanceRole = &ir
		default:
			http.Error(w, "instanceRole: must be admin/member/viewer/billing/pending", http.StatusBadRequest)
			return
		}
	}

	var note *string
	if req.Note != "" {
		n := req.Note
		note = &n
	}

	id := randomID()
	token, err := generateInviteToken()
	if err != nil {
		h.Logger.Error("invite: token", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if err := h.DB.CreateInvite(ctx, db.CreateInviteInput{
		ID:           id,
		Token:        token,
		GroupID:      groupID,
		InstanceRole: instanceRole,
		CreatedBy:    claims.UserID,
		ExpiresAt:    expiresAt,
		MaxUses:      req.MaxUses,
		Note:         note,
	}); err != nil {
		h.Logger.Error("create invite", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	inv, err := h.DB.FindInviteByToken(ctx, token)
	if err != nil {
		h.Logger.Error("re-read invite", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, createInviteResponse{
		Invite: *inv,
		URL:    inviteURL(r, token),
	})
}

// inviteURL returns the public link for an invite. Uses the request's
// host so it works behind the same ingress that served the API call.
func inviteURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host + "/invite/" + token
}

// generateInviteToken produces a 22-char URL-safe token (16 random
// bytes, base64url, no padding). 128 bits of entropy = brute force
// is not the threat model.
func generateInviteToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ---------- List ----------

func (h *InvitesHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !auth.Has(claims.Permissions, auth.PermUserWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx, cancel := invCtx(r)
	defer cancel()
	invs, err := h.DB.ListInvites(ctx)
	if err != nil {
		h.Logger.Error("list invites", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// Annotate each row with the public URL so the admin doesn't have
	// to reconstruct it.
	type withURL struct {
		db.Invite
		URL string `json:"url"`
	}
	out := make([]withURL, 0, len(invs))
	for _, inv := range invs {
		out = append(out, withURL{Invite: inv, URL: inviteURL(r, inv.Token)})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- Revoke ----------

func (h *InvitesHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !auth.Has(claims.Permissions, auth.PermUserWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ctx, cancel := invCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	if err := h.DB.RevokeInvite(ctx, id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("revoke invite", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- Lookup (public) ----------

// inviteSummary is what the signup page reads. NEVER includes the
// admin-only fields (createdBy, internal IDs) — only what's needed
// to render "you've been invited to <group>".
type inviteSummary struct {
	Token        string  `json:"token"`
	GroupID      string  `json:"groupId,omitempty"`
	GroupName    string  `json:"groupName,omitempty"`
	InstanceRole string  `json:"instanceRole,omitempty"`
	ExpiresAt    *string `json:"expiresAt,omitempty"`
	UsesLeft     int     `json:"usesLeft"`
	Note         string  `json:"note,omitempty"`
}

func (h *InvitesHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := invCtx(r)
	defer cancel()
	token := chi.URLParam(r, "token")
	inv, err := h.DB.FindInviteByToken(ctx, token)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "invite not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("lookup invite", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if inv.RevokedAt.Valid {
		http.Error(w, "invite has been revoked", http.StatusGone)
		return
	}
	if inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now()) {
		http.Error(w, "invite has expired", http.StatusGone)
		return
	}
	if inv.UsedCount >= inv.MaxUses {
		http.Error(w, "invite usage cap reached", http.StatusGone)
		return
	}
	out := inviteSummary{
		Token:    inv.Token,
		UsesLeft: inv.MaxUses - inv.UsedCount,
	}
	if inv.GroupID.Valid {
		out.GroupID = inv.GroupID.String
		// Resolve group name for display. Best-effort — if the group
		// was deleted between mint and lookup, we fall back to the ID.
		if g, err := h.DB.GetGroup(ctx, inv.GroupID.String); err == nil {
			out.GroupName = g.Name
		}
	}
	if inv.InstanceRole.Valid {
		out.InstanceRole = inv.InstanceRole.String
	}
	if inv.ExpiresAt.Valid {
		s := inv.ExpiresAt.Time.UTC().Format(time.RFC3339)
		out.ExpiresAt = &s
	}
	if inv.Note.Valid {
		out.Note = inv.Note.String
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- Redeem (local username + password) ----------

type redeemLocalRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type redeemLocalResponse struct {
	AccessToken string `json:"access_token"`
}

// RedeemLocal creates a fresh username+password account, attaches it
// to the invite's group, and returns a JWT. The handler is the
// canonical "first-time signup" path for kuso — no separate
// /register endpoint exists, by design.
func (h *InvitesHandler) RedeemLocal(w http.ResponseWriter, r *http.Request) {
	var req redeemLocalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Token == "" || req.Username == "" || req.Password == "" || req.Email == "" {
		http.Error(w, "token, username, email, password are required", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		http.Error(w, "password must be ≥ 8 chars", http.StatusBadRequest)
		return
	}
	if !validUsername(req.Username) {
		http.Error(w, "username: lowercase letters/digits/underscore/hyphen, ≤32 chars", http.StatusBadRequest)
		return
	}
	ctx, cancel := invCtx(r)
	defer cancel()

	// Pre-flight: make sure the username + email aren't already
	// taken. We do this BEFORE redeeming so an in-use credential
	// doesn't burn an invite seat.
	if _, err := h.DB.FindUserByUsername(ctx, req.Username); err == nil {
		http.Error(w, "username already taken", http.StatusConflict)
		return
	}
	if _, err := h.DB.FindUserByEmail(ctx, req.Email); err == nil {
		http.Error(w, "email already in use", http.StatusConflict)
		return
	}

	hash, err := auth.HashPassword(req.Password, 0)
	if err != nil {
		h.Logger.Error("invite: hash password", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	inv, err := h.DB.RedeemInvite(ctx, req.Token)
	if err != nil {
		h.fail(w, err)
		return
	}

	id := randomHex(16)
	if err := h.DB.CreateUser(ctx, db.CreateUserInput{
		ID:           id,
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		IsActive:     true,
	}); err != nil {
		h.Logger.Error("invite: create user", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Wire group membership. Best-effort — failure here doesn't
	// block sign-in (an admin can fix it later) but we surface the
	// error in logs so it's visible.
	if inv.GroupID.Valid {
		if err := h.DB.AddUserToGroup(ctx, id, inv.GroupID.String); err != nil {
			h.Logger.Warn("invite: add to group", "user", id, "group", inv.GroupID.String, "err", err)
		}
	} else {
		// No group on the invite → drop the user in the pending
		// group so an admin can reach them. Mirrors what OAuth does
		// for un-invited GH signups.
		if err := h.DB.AddUserToPendingGroup(ctx, id); err != nil {
			h.Logger.Warn("invite: pending group", "user", id, "err", err)
		}
	}
	if err := h.DB.RecordRedemption(ctx, inv.ID, id); err != nil {
		h.Logger.Warn("invite: record redemption", "err", err)
	}

	jwt, err := h.signFor(ctx, id, "local")
	if err != nil {
		h.Logger.Error("invite: sign jwt", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, redeemLocalResponse{AccessToken: jwt})
}

// ---------- Redeem via OAuth ----------

// inviteOAuthCookie is defined in oauth.go (the OAuth callback also
// needs to read it). Re-declared at use sites by name only.

func (h *InvitesHandler) RedeemOAuthStart(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "token query param required", http.StatusBadRequest)
		return
	}
	// Validate the invite before kicking the OAuth round-trip so the
	// user gets an immediate 410 instead of a confusing GH error.
	ctx, cancel := invCtx(r)
	defer cancel()
	inv, err := h.DB.FindInviteByToken(ctx, token)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "invite not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if inv.RevokedAt.Valid || (inv.ExpiresAt.Valid && inv.ExpiresAt.Time.Before(time.Now())) ||
		inv.UsedCount >= inv.MaxUses {
		http.Error(w, "invite is no longer redeemable", http.StatusGone)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     inviteOAuthCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	// Bounce to the OAuth start endpoint — the existing GH flow
	// handles the rest. The callback inspects the cookie and runs
	// invite-redemption inline (see oauth.go).
	http.Redirect(w, r, "/api/auth/github", http.StatusFound)
}

// ---------- helpers ----------

func (h *InvitesHandler) signFor(ctx context.Context, userID, strategy string) (string, error) {
	roleName, _ := h.DB.UserRoleName(ctx, userID)
	if roleName == "" {
		roleName = "none"
	}
	groups, _ := h.DB.UserGroupNames(ctx, userID)
	if groups == nil {
		groups = []string{}
	}
	perms, _ := h.DB.UserPermissions(ctx, userID)
	if perms == nil {
		perms = []string{}
	}
	if tenancy, terr := h.DB.ListUserTenancy(ctx, userID); terr == nil {
		for _, p := range auth.Compute(tenancy) {
			already := false
			for _, q := range perms {
				if q == p {
					already = true
					break
				}
			}
			if !already {
				perms = append(perms, p)
			}
		}
	}
	user, err := h.DB.FindUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return h.Issuer.Sign(auth.Claims{
		UserID: user.ID, Username: user.Username, Role: roleName,
		UserGroups: groups, Permissions: perms, Strategy: strategy,
	})
}

func (h *InvitesHandler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "invite not found", http.StatusNotFound)
	case errors.Is(err, db.ErrInviteRevoked):
		http.Error(w, "invite has been revoked", http.StatusGone)
	case errors.Is(err, db.ErrInviteExpired):
		http.Error(w, "invite has expired", http.StatusGone)
	case errors.Is(err, db.ErrInviteExhausted):
		http.Error(w, "invite usage cap reached", http.StatusGone)
	default:
		h.Logger.Error("invite redeem", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

// validUsername mirrors the rule the rest of the kuso surface uses
// for k8s-friendly identifiers: lowercase, alphanumeric + dash + _, ≤32.
func validUsername(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	// Must start with a letter to keep tools that expect identifier-shape happy.
	return strings.IndexByte("abcdefghijklmnopqrstuvwxyz", s[0]) >= 0
}
