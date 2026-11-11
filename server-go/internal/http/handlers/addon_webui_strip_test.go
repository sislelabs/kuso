package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"kuso/server/internal/db"
)

// AH-2: the addon pod must never see the caller's kuso session.
func TestAddonWebUIProxy_StripsKusoCredentials(t *testing.T) {
	var got http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.sig")
	req.Header.Set("Cookie", "kuso.JWT_TOKEN=eyJ.a.b; m=rabbit-session; __Host-kuso_jwt=eyJ.c.d; other=1")
	req.Header.Set("Sec-WebSocket-Protocol", "kuso.bearer, eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.sig, v12.stomp")
	newAddonWebUIProxy(target, "/api/projects/p/addons/mail/webui", "https://kuso.example.com").ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream never called")
	}
	if a := got.Get("Authorization"); a != "" {
		t.Errorf("Authorization forwarded: %q", a)
	}
	if c := got.Get("Cookie"); c != "m=rabbit-session; other=1" {
		t.Errorf("Cookie = %q, want addon cookies only", c)
	}
	if p := got.Get("Sec-WebSocket-Protocol"); p != "v12.stomp" {
		t.Errorf("Sec-WebSocket-Protocol = %q, want v12.stomp", p)
	}
}

// AUTHZ-03: console responses run on the kuso origin, so upstream cookies
// must stay inside the console prefix (never clobber kuso.* session
// cookies), and attacker-influenced documents (mail HTML, message parts)
// must render sandboxed in an opaque origin.
func TestAddonWebUIProxy_ConfinesConsoleResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "kuso.JWT_TOKEN", Value: "attacker", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "m", Value: "rabbit", Path: "/", Domain: "example.com"})
		w.Header().Set("Content-Type", "text/html")
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	const prefix = "/api/projects/p/addons/mail/webui"

	get := func(path string) *http.Response {
		rec := httptest.NewRecorder()
		newAddonWebUIProxy(target, prefix, "https://kuso.example.com").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Result()
	}

	res := get("/")
	cookies := res.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "m" {
		t.Fatalf("cookies = %v, want only the addon's own cookie", res.Header.Values("Set-Cookie"))
	}
	if cookies[0].Path != prefix+"/" || cookies[0].Domain != "" {
		t.Errorf("cookie path/domain = %q/%q, want %q and no domain", cookies[0].Path, cookies[0].Domain, prefix+"/")
	}
	// SEC-6: the shell keeps scripts and same-origin storage (the SPA
	// needs them) but can only reach its own mount and opens no popups.
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"connect-src https://kuso.example.com" + prefix + "/;", "allow-scripts", "allow-same-origin", "form-action "} {
		if !strings.Contains(csp, want) {
			t.Errorf("console shell CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "allow-popups") {
		t.Errorf("console shell CSP %q allows popups, which can read same-origin /api pages", csp)
	}

	for _, p := range []string{"/view/abc.html", "/api/v1/message/abc/part/2"} {
		csp := get(p).Header.Get("Content-Security-Policy")
		if !strings.HasPrefix(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "allow-scripts") {
			t.Errorf("%s: CSP = %q, want a sandbox without scripts or same-origin", p, csp)
		}
	}
}

func TestStripKusoCredentials_KeepsAddonBasicAuth(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Basic Z3Vlc3Q6Z3Vlc3Q=")
	h.Set("Cookie", "kuso.JWT_TOKEN=x")
	stripKusoCredentials(h)
	if h.Get("Authorization") != "Basic Z3Vlc3Q6Z3Vlc3Q=" {
		t.Errorf("addon Basic auth dropped")
	}
	if _, ok := h["Cookie"]; ok {
		t.Errorf("empty Cookie header left behind: %q", h.Get("Cookie"))
	}
}

// AH-2: non-safe methods need Editor; reads stay at Viewer.
func TestAddonWebUIMinRole(t *testing.T) {
	for m, want := range map[string]db.ProjectRole{
		http.MethodGet: db.ProjectRoleViewer, http.MethodHead: db.ProjectRoleViewer,
		http.MethodOptions: db.ProjectRoleViewer, http.MethodPost: db.ProjectRoleEditor,
		http.MethodPut: db.ProjectRoleEditor, http.MethodPatch: db.ProjectRoleEditor,
		http.MethodDelete: db.ProjectRoleEditor,
	} {
		if got := addonWebUIMinRole(m); got != want {
			t.Errorf("%s: got %q want %q", m, got, want)
		}
	}
}

// SEC-8: a page served from an addon console may only reach its own
// console mount, not the rest of /api.
func TestRefuseConsoleOriginatedAPI(t *testing.T) {
	h := RefuseConsoleOriginatedAPI(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	const console = "https://kuso.example.com/api/projects/p/addons/mq/webui/#/queues"
	cases := []struct {
		path, referer string
		want          int
	}{
		{"/api/projects/p/secrets", console, http.StatusForbidden},
		{"/api/projects/p/addons/other/webui/api/overview", console, http.StatusForbidden},
		{"/api/projects/p/addons/mq/webui/api/overview", console, http.StatusNoContent},
		{"/api/projects/p/secrets", "https://kuso.example.com/projects/p", http.StatusNoContent},
		{"/api/projects/p/secrets", "", http.StatusNoContent},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "https://kuso.example.com"+c.path, nil)
		if c.referer != "" {
			r.Header.Set("Referer", c.referer)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s from %q: got %d want %d", c.path, c.referer, rec.Code, c.want)
		}
	}
}
