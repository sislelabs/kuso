package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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

// inviteOAuthCookie pins an invite token to the GH OAuth round-trip
// so the callback knows which invite to redeem after upsert. Mirror
// of the const in invites.go — duplicated here so the OAuth handler
// doesn't import handlers/invites.
const inviteOAuthCookie = "kuso_invite_token"

// MountPublic registers the OAuth start + callback routes onto the
// unauthenticated router group. The flow ends with a redirect to "/"
// carrying the JWT in a cookie, matching the TS behaviour.
func (h *OAuthHandler) MountPublic(r mountable) {
	if h.Github != nil {
		// Init endpoints share the per-IP login bucket (S10 audit fix).
		// Callbacks are not rate-limited because the upstream provider
		// is the trigger; they're already gated by state validation.
		r.Get("/api/auth/github", RateLimitedOAuthStart(h.GithubStart))
		r.Get("/api/auth/github/callback", h.GithubCallback)
	}
	if h.OAuth2 != nil {
		r.Get("/api/auth/oauth2", RateLimitedOAuthStart(h.OAuth2Start))
		r.Get("/api/auth/oauth2/callback", h.OAuth2Callback)
	}
}

// mountable is the small surface a router needs to register OAuth routes.
type mountable interface {
	Get(string, http.HandlerFunc)
}

// GithubStart writes a state cookie + persists the state in the
// OAuthState DB table for single-use enforcement. Redirects to GH.
func (h *OAuthHandler) GithubStart(w http.ResponseWriter, r *http.Request) {
	state, err := auth.NewState()
	if err != nil {
		h.fail(w, "state", err)
		return
	}
	if h.DB != nil {
		if err := h.DB.MintOAuthState(r.Context(), state, ""); err != nil {
			h.fail(w, "mint state", err)
			return
		}
	}
	setStateCookie(w, r, state)
	http.Redirect(w, r, h.Github.AuthCodeURL(state), http.StatusFound)
}

// GithubCallback exchanges the code, upserts the local user, and
// redirects to "/" with the JWT in kuso.JWT_TOKEN.
func (h *OAuthHandler) GithubCallback(w http.ResponseWriter, r *http.Request) {
	// Tolerate a GitHub APP-INSTALL redirect landing here by mistake.
	// When an App is configured with its Setup URL pointed at this OAuth
	// callback (instead of /api/github/setup-callback), GitHub sends the
	// post-install hop here with setup_action/installation_id and NO
	// state — which would fail the state check below with a bare
	// "state mismatch". Those params never appear in a real OAuth
	// sign-in (which carries code+state), so detect them and forward to
	// the install handler, query intact. Self-heals a misconfigured
	// Setup URL; the genuine sign-in path is untouched.
	q := r.URL.Query()
	if q.Get("setup_action") != "" || (q.Get("installation_id") != "" && q.Get("state") == "") {
		target := "/api/github/setup-callback"
		if raw := r.URL.RawQuery; raw != "" {
			target += "?" + raw
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	if !verifyStateCookie(r) {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	// Single-use enforcement — replay protection. ConsumeOAuthState
	// is atomic in Postgres (UPDATE … WHERE consumed=false); the
	// second callback for the same state lands here with 0 rows
	// affected and we bail.
	if h.DB != nil {
		state := r.URL.Query().Get("state")
		if err := h.DB.ConsumeOAuthState(r.Context(), state, 10*time.Minute); err != nil {
			http.Error(w, "state already used or expired", http.StatusBadRequest)
			return
		}
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
	// Invite redemption: if the user came from /api/invites/redeem/oauth/start,
	// the kuso_invite_token cookie pins the GH login to a specific
	// invite. Try to consume it BEFORE upsertAndIssue so the
	// bootstrap-or-pending fallback doesn't drop the user in pending.
	// Failure to redeem is non-fatal: log and continue with the
	// regular login flow so a stale cookie doesn't lock the user
	// out.
	var inviteToConsume *db.Invite
	if c, err := r.Cookie(inviteOAuthCookie); err == nil && c.Value != "" {
		if inv, ierr := h.DB.RedeemInvite(ctx, c.Value); ierr != nil {
			h.Logger.Warn("oauth: invite redeem failed; continuing", "err", ierr)
		} else {
			inviteToConsume = inv
		}
		// Always clear the cookie — single use.
		http.SetCookie(w, &http.Cookie{
			Name: inviteOAuthCookie, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
		})
	}
	jwt, err := h.upsertAndIssueWithInvite(ctx, prof, "oauth2", inviteToConsume)
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
	redirectWithJWT(w, r, jwt)
}

// OAuth2Start kicks off the generic OAuth2 flow.
func (h *OAuthHandler) OAuth2Start(w http.ResponseWriter, r *http.Request) {
	state, err := auth.NewState()
	if err != nil {
		h.fail(w, "state", err)
		return
	}
	if h.DB != nil {
		if err := h.DB.MintOAuthState(r.Context(), state, ""); err != nil {
			h.fail(w, "mint state", err)
			return
		}
	}
	setStateCookie(w, r, state)
	http.Redirect(w, r, h.OAuth2.AuthCodeURL(state), http.StatusFound)
}

// OAuth2Callback handles the generic OAuth2 callback.
func (h *OAuthHandler) OAuth2Callback(w http.ResponseWriter, r *http.Request) {
	if !verifyStateCookie(r) {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	if h.DB != nil {
		state := r.URL.Query().Get("state")
		if err := h.DB.ConsumeOAuthState(r.Context(), state, 10*time.Minute); err != nil {
			http.Error(w, "state already used or expired", http.StatusBadRequest)
			return
		}
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
	redirectWithJWT(w, r, jwt)
}

// upsertAndIssueWithInvite is the invite-aware wrapper. When the
// callback came from a kuso_invite_token cookie, the redemption row
// has already been incremented; we just need to attach the user to
// the invite's group instead of falling through to the generic
// pending-or-admin bootstrap.
//
// Idempotency: a re-attempt of the OAuth callback (browser refresh)
// gets a brand-new invite cookie or none at all, so this method only
// runs the group-attach path when an invite was actually consumed
// this call.
func (h *OAuthHandler) upsertAndIssueWithInvite(ctx context.Context, prof *auth.OAuthProfile, strategy string, inv *db.Invite) (string, error) {
	jwt, err := h.upsertAndIssue(ctx, prof, strategy)
	if err != nil {
		return "", err
	}
	if inv == nil {
		return jwt, nil
	}
	uid, _ := h.userIDForProvider(ctx, prof)
	if uid == "" {
		return jwt, nil
	}
	if inv.GroupID.Valid {
		if err := h.DB.AddUserToGroup(ctx, uid, inv.GroupID.String); err != nil {
			h.Logger.Warn("invite oauth: add to group", "user", uid, "group", inv.GroupID.String, "err", err)
		}
	}
	if err := h.DB.RecordRedemption(ctx, inv.ID, uid); err != nil {
		h.Logger.Warn("invite oauth: record redemption", "err", err)
	}
	// We need to RE-ISSUE the JWT so the new group's permissions land
	// in the claims. Otherwise the user would have to log out + in to
	// see their new tenancy.
	freshJWT, err := h.upsertAndIssue(ctx, prof, strategy)
	if err != nil {
		// If re-sign fails, return the original — the user is still
		// logged in, they just won't see new perms until next login.
		return jwt, nil
	}
	return freshJWT, nil
}

// upsertAndIssue finds-or-creates a kuso User row from the OAuth profile
// and returns a signed JWT carrying their permissions.
func (h *OAuthHandler) upsertAndIssue(ctx context.Context, prof *auth.OAuthProfile, strategy string) (string, error) {
	if h.DB == nil {
		return "", errors.New("oauth: DB not wired")
	}
	user, err := h.DB.FindUserByUsername(ctx, prof.Username)
	created := false
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
		created = true
	}
	// Sync the OAuth provider's avatar onto the kuso user row so the
	// profile page + nav avatar render the GitHub/Google picture.
	// Runs on every login (not just first-create) so a refreshed
	// avatar URL or a newly-set picture lands in the DB. Empty prof
	// images are skipped — uploaded local avatars (data: URLs) survive
	// because we only overwrite when there's something fresh to write.
	if prof.Image != "" && (!user.Image.Valid || user.Image.String != prof.Image) {
		img := prof.Image
		if err := h.DB.UpdateUser(ctx, user.ID, db.UpdateUserInput{Image: &img}); err != nil {
			h.Logger.Warn("oauth: persist avatar", "err", err, "user", user.ID)
		} else {
			user.Image.String = img
			user.Image.Valid = true
		}
	}
	// Bootstrap: pick a group for this user. Runs on EVERY login, not
	// just newly-created accounts — that catches the regression where
	// a first-OAuth-login on a pre-tenancy build created the user
	// without bootstrapping, and every subsequent login skipped the
	// bootstrap because the user already existed.
	//
	// PromoteUserToAdminIfNoAdmin is the core: if the cluster has
	// zero admin-group members, the current user becomes admin. So
	// the first person to log in to a fresh install always gets
	// admin, regardless of which version they're on when they do.
	if err := h.bootstrapOrPending(ctx, user.ID); err != nil {
		h.Logger.Warn("oauth: bootstrap user", "err", err, "user", user.ID)
	}
	_ = created // retained for future audit logging
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
	// Union tenancy-derived perms (instanceRole + projectMemberships)
	// with the role-perms pivot, same as the password-login flow.
	// Keeps OAuth + password identical post-bootstrap.
	if tenancy, terr := h.DB.ListUserTenancy(ctx, user.ID); terr == nil {
		for _, p := range auth.Compute(tenancy) {
			if !containsStr(perms, p) {
				perms = append(perms, p)
			}
		}
	}
	// Honor the admin-configurable session lifetime (Setting table,
	// session.* keys), same as the password-login path in auth.go, so
	// OAuth sessions don't silently keep the old 10h expiry. Best-
	// effort read; falls back to the 30-day default.
	tok, _, err := signSessionToken(ctx, h.DB, h.Issuer, auth.Claims{
		UserID: user.ID, Username: user.Username, Role: roleName,
		UserGroups: groups, Permissions: perms, Strategy: strategy,
	})
	return tok, err
}

// bootstrapOrPending decides where a fresh OAuth user lands. We try
// in this order:
//
//  1. Disaster recovery: if no admin group member exists in the whole
//     cluster, promote this user to admin. Covers two cases —
//       (a) first OAuth login on a fresh install (admin group exists
//           empty after EnsureAdminGroup, no seed admin user), and
//       (b) the seed admin was deleted and someone needs to take over.
//  2. Otherwise drop them in the pending group so an admin can grant
//     access without them stumbling around the UI.
//
// Idempotent: re-running just re-attaches to whichever group they
// already belong to (INSERT OR IGNORE on the pivot).
func (h *OAuthHandler) bootstrapOrPending(ctx context.Context, userID string) error {
	promoted, err := h.DB.PromoteUserToAdminIfNoAdmin(ctx, userID)
	if err != nil {
		return err
	}
	if promoted {
		h.Logger.Info("oauth: promoted to admin (no other admins)", "user", userID)
		return nil
	}
	// Don't pile users into pending if they're already in any group
	// — that includes existing admins, project members, and even
	// users who were already pending (no point inserting twice).
	groups, gerr := h.DB.UserGroupNames(ctx, userID)
	if gerr == nil && len(groups) > 0 {
		return nil
	}
	gid := "grp-pending"
	_ = h.DB.CreateGroup(ctx, gid, "kuso-pending", "users awaiting admin approval")
	if err := h.DB.SetGroupTenancy(ctx, gid, db.GroupTenancy{
		InstanceRole: db.InstanceRolePending,
	}); err != nil {
		return err
	}
	return h.DB.AddUserToGroup(ctx, userID, gid)
}

// containsStr is a local copy of the helper in auth.go to avoid the
// circular tug-of-war if we hoist it into a shared util.
func containsStr(haystack []string, s string) bool {
	for _, h := range haystack {
		if h == s {
			return true
		}
	}
	return false
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

func setStateCookie(w http.ResponseWriter, r *http.Request, state string) {
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: 600,
		// Secure tracks the actual scheme so OAuth works on plain-HTTP
		// dev hosts (LAN-IP, un-TLS'd) the same way password login does
		// (auth.go uses isHTTPS); production stays TLS-terminated → Secure.
		Secure: isHTTPS(r),
	})
}

func verifyStateCookie(r *http.Request) bool {
	c, err := r.Cookie(stateCookie)
	if err != nil || c.Value == "" {
		return false
	}
	// Constant-time compare so a timing oracle can't shave bits off
	// the state value over many forged callbacks. The string length
	// is also leaked by ConstantTimeCompare returning early on
	// mismatch, so first check lengths match in constant time too.
	q := r.URL.Query().Get("state")
	if len(q) != len(c.Value) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(q), []byte(c.Value)) == 1
}

// setJWTCookie writes the kuso.JWT_TOKEN cookie used by the WebSocket
// log tail (which needs the bearer in a header the SPA can't set).
//
// HttpOnly is true now — XSS in the SPA can no longer steal the
// session token. The SPA receives the JWT via a URL fragment after
// OAuth (see redirectWithJWT) and stores it in localStorage; api()
// reads localStorage directly. The cookie exists only as a server-
// readable bearer for the WS handshake, where the browser can't
// send Authorization headers.
//
// Secure tracks the request scheme (isHTTPS) so the browser ships the
// cookie over TLS in production but the session also lands on plain-HTTP
// dev hosts — matching the password-login path in auth.go. Without this
// the OAuth session cookie was silently dropped on non-localhost HTTP,
// so login appeared to succeed then bounced straight back to /login.
// SameSite=Lax keeps the OAuth-callback redirect path working while
// blocking cross-site CSRF.
func setJWTCookie(w http.ResponseWriter, r *http.Request, jwt string) {
	http.SetCookie(w, &http.Cookie{
		Name: "kuso.JWT_TOKEN", Value: jwt, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: isHTTPS(r),
		// MaxAge tracks the token's own exp claim (driven by the
		// admin-configurable session setting) instead of a hardcoded
		// 10h, so a longer/never-expiring session isn't truncated by a
		// short cookie. A token with no exp (never-expire) → 10y cookie.
		MaxAge: cookieMaxAgeFromJWT(jwt),
	})
}

// cookieMaxAgeFromJWT reads the unverified exp claim off a freshly-
// minted token to size the session cookie. We just signed this token so
// no signature check is needed here — we only want its expiry. Returns
// a 10-year MaxAge when the token has no exp (never-expire sessions),
// and the 30-day default if the claim can't be read.
func cookieMaxAgeFromJWT(tok string) int {
	const tenYears = 10 * 365 * 24 * 60 * 60
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return db.DefaultSessionTTLSeconds
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return db.DefaultSessionTTLSeconds
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return db.DefaultSessionTTLSeconds
	}
	if claims.Exp == 0 {
		return tenYears // never-expire token
	}
	secs := claims.Exp - time.Now().Unix()
	if secs < 60 {
		secs = 60
	}
	return int(secs)
}

// redirectWithJWT finalises an OAuth flow by setting the HttpOnly
// session cookie and bouncing the browser to "/?login=ok". The SPA
// reads session identity via /api/auth/session (which rides the
// cookie); JS never sees the JWT bytes.
//
// Previous implementation emitted "/#token=<jwt>" so the SPA could
// stash the JWT in localStorage for the WebSocket subprotocol
// bearer. That path is closed: the WS upgrade also reads the
// kuso.JWT_TOKEN cookie now (see logs_ws.go), so fragment delivery
// is dead code. Closing it removes the window where the token sits
// in browser history / analytics referer / third-party scripts on
// the landing page.
func redirectWithJWT(w http.ResponseWriter, r *http.Request, jwt string) {
	setJWTCookie(w, r, jwt)
	http.Redirect(w, r, "/?login=ok", http.StatusFound)
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
