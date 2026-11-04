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
	req.Header.Set("Cookie", "kuso.JWT_TOKEN=eyJ.a.b; m=rabbit-session; other=1")
	req.Header.Set("Sec-WebSocket-Protocol", "kuso.bearer, eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1In0.sig, v12.stomp")
	newAddonWebUIProxy(target, "/api/projects/p/addons/mail/webui").ServeHTTP(httptest.NewRecorder(), req)

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
		newAddonWebUIProxy(target, prefix).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
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
	if csp := res.Header.Get("Content-Security-Policy"); csp != "" {
		t.Errorf("console shell got CSP %q; sandboxing it would break the SPA", csp)
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
