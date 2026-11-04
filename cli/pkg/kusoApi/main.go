// Package kusoApi is the HTTP client the kuso CLI uses to talk to a
// kuso server. Thin wrapper over resty: every endpoint is a one-line
// method that returns the raw *resty.Response so callers can decide
// how to decode (most use json.Unmarshal on resp.Body()).
//
// All requests carry a bearer token set by SetApiUrl. The token comes
// from ~/.kuso/credentials.yaml after `kuso login`.
package kusoApi

import (
	"crypto/tls"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

//go:embed VERSION
var embeddedVersion string

// KusoClient wraps the resty client the CLI reuses across calls so every
// method is just `client.Method(path)`.
type KusoClient struct {
	baseURL     string
	bearerToken string
	host        string
	client      *request
}

// request keeps the `k.client.SetBody(x); k.client.Post(p)` call shape
// while building a fresh *resty.Request per HTTP call. A single shared
// resty.Request kept the last SetBody/SetHeader and re-sent it on the
// next body-less POST/PUT/DELETE in the same process.
type request struct {
	rc              *resty.Client
	token, host, ua string
	// Body / headers are pending state for the next verb call only.
	Body    any
	headers map[string]string
}

func (r *request) base() *resty.Request {
	return r.rc.R().
		SetAuthScheme("Bearer").
		SetAuthToken(r.token).
		SetHeader("Host", r.host).
		SetHeader("Accept", "application/json").
		SetHeader("Content-Type", "application/json").
		SetHeader("User-Agent", r.ua)
}

func (r *request) SetBody(body any) *request {
	r.Body = body
	return r
}

func (r *request) SetHeader(key, value string) *request {
	if r.headers == nil {
		r.headers = map[string]string{}
	}
	r.headers[key] = value
	return r
}

// take returns a fresh request carrying the pending body/headers and
// clears them so they apply to exactly one call.
func (r *request) take() *resty.Request {
	req := r.base()
	if r.Body != nil {
		req.SetBody(r.Body)
	}
	for k, v := range r.headers {
		req.SetHeader(k, v)
	}
	r.Body, r.headers = nil, nil
	return req
}

func (r *request) Get(url string) (*resty.Response, error)    { return r.take().Get(url) }
func (r *request) Post(url string) (*resty.Response, error)   { return r.take().Post(url) }
func (r *request) Put(url string) (*resty.Response, error)    { return r.take().Put(url) }
func (r *request) Patch(url string) (*resty.Response, error)  { return r.take().Patch(url) }
func (r *request) Delete(url string) (*resty.Response, error) { return r.take().Delete(url) }

// Sentinel errors callers check via errors.Is.
var (
	ErrNotConfigured   = errors.New("kuso is not configured — run `kuso login --api <instance-url>` first")
	ErrUnauthenticated = errors.New("not authenticated: run `kuso login`")
)

// Init wires up the client with a base URL + bearer token and returns
// a fresh pre-authenticated resty.Request so legacy callers can chain on it.
// Most code should pass through one of the typed methods (GetProjects,
// CreateBuild, …) instead of touching the request directly.
func (k *KusoClient) Init(baseURL, bearerToken string) *resty.Request {
	k.SetApiUrl(baseURL, bearerToken)
	return k.client.base()
}

// SetApiUrl reconfigures the client for a new instance. Called by
// `kuso login` and `kuso remote select` so the user can flip between
// kuso instances without restarting.
//
// Special case: localhost URLs route to localhost:<port> with a
// "kuso.localhost" Host header. Lets developers run a local server on
// a vhost name without reaching for /etc/hosts edits.
func (k *KusoClient) SetApiUrl(apiURL, bearerToken string) {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		// Fall back to the raw input — the user's next request will
		// fail with a clearer error than parse-stage panic.
		k.baseURL = apiURL
		k.host = apiURL
	} else if h := strings.ToLower(parsed.Hostname()); h == "localhost" || strings.HasSuffix(h, ".localhost") {
		k.baseURL = parsed.Scheme + "://localhost"
		if parsed.Port() != "" {
			k.baseURL += ":" + parsed.Port()
		}
		k.host = "kuso.localhost"
	} else {
		k.baseURL = apiURL
		k.host = parsed.Host
	}

	ua := "kuso-cli/" + strings.TrimSpace(embeddedVersion)
	// Per-request timeout. Long enough to absorb a slow build-poller
	// tick on a busy cluster; short enough that a wedged server
	// doesn't hang `kuso ...` indefinitely. The previous (no-timeout)
	// default meant CI runs would block forever on a stalled API.
	// Override with KUSO_CLI_TIMEOUT=<go-duration> (e.g. "30s", "5m").
	timeout := 60 * time.Second
	if v := strings.TrimSpace(os.Getenv("KUSO_CLI_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	rc := resty.New().SetBaseURL(k.baseURL).SetTimeout(timeout)
	// Central not-configured gate. Execute() always constructs the
	// client — even with no saved instance — so a fresh install used to
	// surface resty's raw `Get "/api/projects": unsupported protocol
	// scheme ""` on every command. Every one of the ~216 typed methods
	// funnels through this resty client, so one request middleware
	// covers them all: empty base URL → ErrNotConfigured (resty unwraps
	// its internal retry wrapper before returning, so errors.Is still
	// matches the sentinel).
	rc.OnBeforeRequest(func(_ *resty.Client, _ *resty.Request) error {
		if strings.TrimSpace(k.baseURL) == "" {
			return ErrNotConfigured
		}
		return nil
	})
	// Fresh installs default to LE *staging* certs — the browser warns
	// and Go's http.Client outright rejects. We honor KUSO_INSECURE=1
	// so the same `kuso login …` shown in the install footer actually
	// works against a brand-new box, without requiring the user to
	// figure out cert plumbing on day one. Off by default; once the
	// instance is flipped to LE prod, unset it.
	if v := strings.TrimSpace(os.Getenv("KUSO_INSECURE")); v == "1" || strings.EqualFold(v, "true") {
		rc = rc.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
	}
	k.client = &request{rc: rc, token: bearerToken, host: k.host, ua: ua}
	k.bearerToken = bearerToken
}

// BaseURL returns the configured kuso API root. The logs --follow
// path needs this to derive a ws:// URL from the same instance config
// the rest of the CLI is using.
func (k *KusoClient) BaseURL() string { return k.baseURL }

// BearerToken exposes the JWT so the WebSocket dialer can pass it as
// a Sec-WebSocket-Protocol value (browsers can't set Authorization on
// WS upgrades; we follow the same convention).
func (k *KusoClient) BearerToken() string { return k.bearerToken }

// ensureReady returns an error if the client wasn't initialized via
// Init/SetApiUrl. Used by methods that perform real I/O.
func (k *KusoClient) ensureReady() error {
	if k == nil || k.client == nil || k.baseURL == "" {
		return ErrNotConfigured
	}
	return nil
}

// ---------- Auth ----------

// Login posts username/password to /api/auth/login. The response body
// carries the access_token the CLI persists into credentials.yaml.
func (k *KusoClient) Login(username, password string) (*resty.Response, error) {
	if err := k.ensureReady(); err != nil {
		return nil, err
	}
	k.client.SetBody(map[string]string{"username": username, "password": password})
	return k.client.Post("/api/auth/login")
}

// ---------- Generic ----------

// RawGet hits an arbitrary path under the configured base URL. Used
// by the few endpoints that don't have a typed wrapper yet (system
// version checks, etc.). Path must start with "/".
func (k *KusoClient) RawGet(path string) (*resty.Response, error) {
	if err := k.ensureReady(); err != nil {
		return nil, err
	}
	return k.client.Get(path)
}
