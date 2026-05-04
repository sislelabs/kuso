package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// OAuthProfile is the slim shape both GitHub and generic-OAuth2 paths
// converge on. The login flow uses Username/Email/ProviderID to upsert
// the kuso User row.
type OAuthProfile struct {
	Provider   string
	ProviderID string
	Username   string
	Email      string
	Image      string
	Login      string // GitHub-specific; same as Username for GitHub
}

// GithubOAuth bundles the env config the GitHub strategy needs.
//
// APIBase overrides the GitHub API root for /user and /user/emails
// requests. Empty defaults to https://api.github.com. Set in tests to
// point at an httptest.Server that mocks the GitHub responses; the
// real OAuth endpoint is overridden via Cfg.Endpoint at the same site.
type GithubOAuth struct {
	Cfg     *oauth2.Config
	APIBase string
}

// NewGithubOAuth reads GITHUB_CLIENT_{ID,SECRET,CALLBACKURL,SCOPE} from
// env and returns nil when not configured.
//
// Convenience: a configured kuso-github-app Secret already has the
// same OAuth credentials kuso-server needs (every GitHub App has both
// webhook/installation creds AND OAuth client creds). To avoid making
// the admin paste the same client_id+secret twice, fall back to the
// GITHUB_APP_CLIENT_{ID,SECRET} keys when the GITHUB_CLIENT_* vars are
// blank. CALLBACKURL is also auto-derived from KUSO_DOMAIN when missing
// — admins almost never want to override it past install time.
func NewGithubOAuth() *GithubOAuth {
	id := firstNonEmpty(
		os.Getenv("GITHUB_CLIENT_ID"),
		os.Getenv("GITHUB_APP_CLIENT_ID"),
	)
	secret := firstNonEmpty(
		os.Getenv("GITHUB_CLIENT_SECRET"),
		os.Getenv("GITHUB_APP_CLIENT_SECRET"),
	)
	cb := os.Getenv("GITHUB_CLIENT_CALLBACKURL")
	if cb == "" {
		if domain := os.Getenv("KUSO_DOMAIN"); domain != "" {
			cb = "https://" + domain + "/api/auth/github/callback"
		}
	}
	if id == "" || secret == "" || cb == "" {
		return nil
	}
	scopes := splitScopes(os.Getenv("GITHUB_CLIENT_SCOPE"))
	if len(scopes) == 0 {
		scopes = []string{"read:user", "user:email"}
	}
	return &GithubOAuth{Cfg: &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		RedirectURL:  cb,
		Scopes:       scopes,
		Endpoint:     github.Endpoint,
	}}
}

// firstNonEmpty returns the first non-empty string from its args. Used
// to walk a fallback chain of env vars without nesting "if x != \"\"".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// AuthCodeURL returns the URL the browser should redirect to. state is
// generated fresh per call and the caller is responsible for storing
// it in a short-lived cookie to verify on callback.
func (g *GithubOAuth) AuthCodeURL(state string) string {
	return g.Cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange swaps the code for a token and fetches the user profile.
func (g *GithubOAuth) Exchange(ctx context.Context, code string) (*OAuthProfile, *oauth2.Token, error) {
	tok, err := g.Cfg.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("github oauth: exchange: %w", err)
	}
	cli := g.Cfg.Client(ctx, tok)
	prof, err := fetchGithubUser(ctx, cli, g.apiBase())
	if err != nil {
		return nil, tok, err
	}
	return prof, tok, nil
}

func (g *GithubOAuth) apiBase() string {
	if g.APIBase != "" {
		return g.APIBase
	}
	return "https://api.github.com"
}

func fetchGithubUser(ctx context.Context, cli *http.Client, apiBase string) (*OAuthProfile, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/user", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github user: %s: %s", resp.Status, string(body))
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("github user decode: %w", err)
	}
	prof := &OAuthProfile{
		Provider:   "github",
		ProviderID: fmt.Sprintf("%d", u.ID),
		Username:   u.Login,
		Login:      u.Login,
		Email:      u.Email,
		Image:      u.AvatarURL,
	}
	if prof.Email == "" {
		// Email is hidden by default — fall back to /user/emails.
		prof.Email = fetchGithubPrimaryEmail(ctx, cli, apiBase)
	}
	if prof.Username == "" && prof.Email != "" {
		prof.Username = prof.Email
	}
	if prof.Username == "" {
		return nil, errors.New("github: no username or email on profile")
	}
	return prof, nil
}

func fetchGithubPrimaryEmail(ctx context.Context, cli *http.Client, apiBase string) string {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/user/emails", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := cli.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return ""
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return ""
	}
	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	for _, e := range emails {
		if e.Verified {
			return e.Email
		}
	}
	return ""
}

// GenericOAuth bundles env-driven config for a generic OAuth2 provider.
type GenericOAuth struct {
	Cfg     *oauth2.Config
	UserURL string // OAUTH2_CLIENT_USER_INFO_URL
}

// NewGenericOAuth reads OAUTH2_CLIENT_* env. Returns nil when not
// configured.
func NewGenericOAuth() *GenericOAuth {
	id := os.Getenv("OAUTH2_CLIENT_ID")
	secret := os.Getenv("OAUTH2_CLIENT_SECRET")
	cb := os.Getenv("OAUTH2_CLIENT_CALLBACKURL")
	authURL := os.Getenv("OAUTH2_CLIENT_AUTH_URL")
	tokenURL := os.Getenv("OAUTH2_CLIENT_TOKEN_URL")
	if id == "" || secret == "" || cb == "" || authURL == "" || tokenURL == "" {
		return nil
	}
	userURL := os.Getenv("OAUTH2_CLIENT_USER_INFO_URL")
	scopes := splitScopes(os.Getenv("OAUTH2_CLIENT_SCOPE"))
	return &GenericOAuth{
		Cfg: &oauth2.Config{
			ClientID:     id,
			ClientSecret: secret,
			RedirectURL:  cb,
			Scopes:       scopes,
			Endpoint:     oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
		},
		UserURL: userURL,
	}
}

// AuthCodeURL returns the URL the browser should redirect to.
func (g *GenericOAuth) AuthCodeURL(state string) string {
	return g.Cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange swaps the code and fetches the user profile from UserURL.
func (g *GenericOAuth) Exchange(ctx context.Context, code string) (*OAuthProfile, *oauth2.Token, error) {
	tok, err := g.Cfg.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("oauth2: exchange: %w", err)
	}
	if g.UserURL == "" {
		return nil, tok, errors.New("oauth2: OAUTH2_CLIENT_USER_INFO_URL not set")
	}
	cli := g.Cfg.Client(ctx, tok)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.UserURL, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return nil, tok, fmt.Errorf("oauth2 userinfo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, tok, fmt.Errorf("oauth2 userinfo: %s: %s", resp.Status, string(body))
	}
	var u map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, tok, fmt.Errorf("oauth2 userinfo decode: %w", err)
	}
	prof := &OAuthProfile{Provider: "oauth2"}
	prof.ProviderID = firstString(u, "sub", "id")
	prof.Username = firstString(u, "preferred_username", "username", "login", "email")
	prof.Email = firstString(u, "email")
	prof.Image = firstString(u, "picture", "avatar_url")
	if prof.Username == "" {
		prof.Username = prof.Email
	}
	if prof.Username == "" {
		return nil, tok, errors.New("oauth2: profile missing username + email")
	}
	if prof.ProviderID == "" {
		prof.ProviderID = prof.Username
	}
	return prof, tok, nil
}

// splitScopes splits a space- or comma-separated scope string.
func splitScopes(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, sep := range []string{",", " "} {
		if strings.Contains(s, sep) {
			parts := strings.Split(s, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return []string{s}
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
		if v, ok := m[k].(float64); ok {
			return fmt.Sprintf("%d", int64(v))
		}
	}
	return ""
}

// NewState returns a 32-byte hex string suitable for OAuth2 state values.
func NewState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
