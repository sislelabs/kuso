package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
	newAddonWebUIProxy(target).ServeHTTP(httptest.NewRecorder(), req)

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
