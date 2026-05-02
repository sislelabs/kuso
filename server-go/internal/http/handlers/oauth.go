package handlers

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// OAuthHandler hosts /api/auth/github and /api/auth/oauth2 plus their
// callbacks. Both flows converge on the same upsert + JWT-issue logic.
type OAuthHandler struct {
	DB     *db.DB
	Issuer *auth.Issuer
	Github *auth.GithubOAuth
	OAuth2 *auth.GenericOAuth
	Logger *slog.Logger
}

// stateCookie is the short-lived cookie name for OAuth state values.
const stateCookie = "kuso_oauth_state"

// MountPublic registers the OAuth start + callback routes onto the
// unauthenticated router group. The flow ends with a redirect to "/"
// carrying the JWT in a cookie, matching the TS behaviour.
func (h *OAuthHandler) MountPublic(r mountable) {
	if h.Github != nil {
		r.Get("/api/auth/github", h.GithubStart)
		r.Get("/api/auth/github/callback", h.GithubCallback)
	}
	if h.OAuth2 != nil {
		r.Get("/api/auth/oauth2", h.OAuth2Start)
		r.Get("/api/auth/oauth2/callback", h.OAuth2Callback)
	}
}

// mountable is the small surface a router needs to register OAuth routes.
type mountable interface {
	Get(string, http.HandlerFunc)
}

// GithubStart writes a state cookie and redirects to GitHub's authorize
// page.
func (h *OAuthHandler) GithubStart(w http.ResponseWriter, r *http.Request) {
	state, err := auth.NewState()
	if err != nil {
		h.fail(w, "state", err)
		return
	}
	setStateCookie(w, state)
	http.Redirect(w, r, h.Github.AuthCodeURL(state), http.StatusFound)
}

// GithubCallback exchanges the code, upserts the local user, and
// redirects to "/" with the JWT in kuso.JWT_TOKEN.
func (h *OAuthHandler) GithubCallback(w http.ResponseWriter, r *http.Request) {
	if !verifyStateCookie(r) {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	prof, tok, err := h.Github.Exchange(ctx, code)
	if err != nil {
		h.fail(w, "github exchange", err)
		return
	}
	jwt, err := h.upsertAndIssue(ctx, prof, "oauth2")
	if err != nil {
		h.fail(w, "issue jwt", err)
		return
	}
	if h.DB != nil && tok != nil {
		// Persist the GithubUserLink so the project flow knows which
		// installations this user can see.
		userID, _ := h.userIDForProvider(ctx, prof)
		if userID != "" {
			ghID, _ := strconv.ParseInt(prof.ProviderID, 10, 64)
			access := tok.AccessToken
			_ = h.DB.UpsertGithubUserLink(ctx, db.GithubUserLink{
				UserID: userID, GithubLogin: prof.Login, GithubID: ghID,
				AccessToken: nullStringFrom(access),
			})
		}
	}
	setJWTCookie(w, jwt)
	http.Redirect(w, r, "/", http.StatusFound)
}

// OAuth2Start kicks off the generic OAuth2 flow.
func (h *OAuthHandler) OAuth2Start(w http.ResponseWriter, r *http.Request) {
	state, err := auth.NewState()
	if err != nil {
		h.fail(w, "state", err)
		return
	}
	setStateCookie(w, state)
	http.Redirect(w, r, h.OAuth2.AuthCodeURL(state), http.StatusFound)
}

// OAuth2Callback handles the generic OAuth2 callback.
func (h *OAuthHandler) OAuth2Callback(w http.ResponseWriter, r *http.Request) {
	if !verifyStateCookie(r) {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	prof, _, err := h.OAuth2.Exchange(ctx, code)
	if err != nil {
		h.fail(w, "oauth2 exchange", err)
		return
	}
	jwt, err := h.upsertAndIssue(ctx, prof, "oauth2")
	if err != nil {
		h.fail(w, "issue jwt", err)
		return
	}
	setJWTCookie(w, jwt)
	http.Redirect(w, r, "/", http.StatusFound)
}

// upsertAndIssue finds-or-creates a kuso User row from the OAuth profile
// and returns a signed JWT carrying their permissions.
func (h *OAuthHandler) upsertAndIssue(ctx context.Context, prof *auth.OAuthProfile, strategy string) (string, error) {
	if h.DB == nil {
		return "", errors.New("oauth: DB not wired")
	}
	user, err := h.DB.FindUserByUsername(ctx, prof.Username)
	if err != nil {
		// Create a stub local user. Password hash is set to a bcrypt of
		// a random secret so password login is impossible — the user is
		// OAuth-only until they set a password through the UI.
		dummy, _ := auth.HashPassword(randomHex(32), 4)
		id := randomHex(16)
		isActive := true
		if err := h.DB.CreateUser(ctx, db.CreateUserInput{
			ID: id, Username: prof.Username, Email: prof.Email, PasswordHash: dummy, IsActive: isActive,
		}); err != nil {
			return "", fmt.Errorf("create user: %w", err)
		}
		user, err = h.DB.FindUserByID(ctx, id)
		if err != nil {
			return "", fmt.Errorf("re-read user: %w", err)
		}
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
	return h.Issuer.Sign(auth.Claims{
		UserID: user.ID, Username: user.Username, Role: roleName,
		UserGroups: groups, Permissions: perms, Strategy: strategy,
	})
}

func (h *OAuthHandler) userIDForProvider(ctx context.Context, prof *auth.OAuthProfile) (string, error) {
	u, err := h.DB.FindUserByUsername(ctx, prof.Username)
	if err != nil {
		return "", err
	}
	return u.ID, nil
}

func (h *OAuthHandler) fail(w http.ResponseWriter, op string, err error) {
	h.Logger.Error("oauth handler", "op", op, "err", err)
	http.Error(w, "internal", http.StatusInternalServerError)
}

func setStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 600,
		Secure: true,
	})
}

func verifyStateCookie(r *http.Request) bool {
	c, err := r.Cookie(stateCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return r.URL.Query().Get("state") == c.Value
}

// setJWTCookie writes the kuso.JWT_TOKEN cookie matching the TS server's
// shape. SameSite=Lax + Secure so the browser only sends it over TLS.
//
// HttpOnly is intentionally false: the SPA reads this cookie from JS
// (vue3-cookies) to attach it as an Authorization: Bearer header on
// every /api request. Making it HttpOnly here breaks the entire SPA
// after OAuth login — the browser holds the cookie but the SPA can't
// see it, so every /api/* call comes back 401.
func setJWTCookie(w http.ResponseWriter, jwt string) {
	http.SetCookie(w, &http.Cookie{
		Name: "kuso.JWT_TOKEN", Value: jwt, Path: "/",
		HttpOnly: false, SameSite: http.SameSiteLaxMode,
		Secure: true,
		MaxAge: 36000,
	})
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// nullStringFrom wraps an optional string in sql.NullString. Empty
// input → Valid=false.
func nullStringFrom(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
