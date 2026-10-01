package spa

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// nextExportFS mimics the layout `next build` emits with `output: "export"`:
// each route produces both <route>.html and a <route>/ directory holding
// RSC streaming payloads. Plus dynamic segments: [project] is exported
// as "_.html" + "_/" with another _.html for nested dynamic routes.
// This is the layout the spa Handler must resolve correctly.
func nextExportFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                    {Data: []byte("<html>landing</html>")},
		"login.html":                    {Data: []byte("<html>login</html>")},
		"login/__next._head.txt":        {Data: []byte("rsc")},
		"projects/new.html":             {Data: []byte("<html>new project</html>")},
		"projects/new/__next._head.txt": {Data: []byte("rsc")},
		"projects/new/__next.!KGFwcCk.projects.new.txt": {Data: []byte("rsc")},
		"projects/_.html":             {Data: []byte("<html>project detail</html>")},
		"projects/_/services/_.html":  {Data: []byte("<html>service detail</html>")},
		"_next/static/chunks/main.js": {Data: []byte("// js")},
		"favicon.ico":                 {Data: []byte("ico")},
	}
}

// drive issues req against the handler and returns (status, body).
func drive(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code, rr.Body.String()
}

func TestHandler_RootServesIndex(t *testing.T) {
	h, err := Handler(nextExportFS(), "/api/")
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	code, body := drive(t, h, http.MethodGet, "/")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "landing") {
		t.Errorf("body: %q", body)
	}
}

func TestHandler_NextRoute_NoSlash(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/projects/new")
	if code != 200 {
		t.Fatalf("status: %d body=%q", code, body)
	}
	if !strings.Contains(body, "new project") {
		t.Errorf("served wrong file: %q", body)
	}
}

func TestHandler_NextRoute_TrailingSlash(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/projects/new/")
	if code != 200 {
		t.Fatalf("status: %d body=%q", code, body)
	}
	if !strings.Contains(body, "new project") {
		t.Errorf("served wrong file: %q", body)
	}
}

func TestHandler_NextRoute_ExplicitHTML(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/projects/new.html")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "new project") {
		t.Errorf("body: %q", body)
	}
}

func TestHandler_RSCPayloads(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/projects/new/__next._head.txt")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if body != "rsc" {
		t.Errorf("body: %q", body)
	}
}

func TestHandler_StaticAsset(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/_next/static/chunks/main.js")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if body != "// js" {
		t.Errorf("body: %q", body)
	}
}

func TestHandler_DynamicSegmentServesUnderscoreFallback(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	// /projects/<slug> must serve projects/_.html (the dynamic-segment
	// placeholder), NOT the root index.html. Without this, the client-
	// side router lands the marketing landing on top of /projects/<slug>,
	// the landing's "if signed in, replace to /projects" effect bounces
	// the user out of the project page.
	code, body := drive(t, h, http.MethodGet, "/projects/kuso-hello-go")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "project detail") {
		t.Errorf("dynamic fallback served wrong file: %q", body)
	}
	if strings.Contains(body, "landing") {
		t.Error("served root landing instead of projects/_.html")
	}
}

func TestHandler_NestedDynamicSegmentServesDeepUnderscoreFallback(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, body := drive(t, h, http.MethodGet, "/projects/abc/services/web")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "service detail") {
		t.Errorf("nested fallback served wrong file: %q", body)
	}
}

func TestHandler_NestedDynamicWithSecondSegmentMissingClimbsUp(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	// /projects/abc/unknown-leaf has no projects/_/unknown-leaf.html
	// AND no projects/_/_.html — the resolver should climb back to
	// projects/_.html (the closest dynamic placeholder that does exist).
	// This keeps deep links viable without requiring the export to
	// pre-render every leaf path.
	code, body := drive(t, h, http.MethodGet, "/projects/abc/some-leaf-with-no-static-route")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "project detail") {
		t.Errorf("expected projects/_.html (project detail), got: %q", body)
	}
}

func TestHandler_UnknownURLIs404(t *testing.T) {
	fsys := nextExportFS()
	fsys["404.html"] = &fstest.MapFile{Data: []byte("<html>not found page</html>")}
	h, _ := Handler(fsys, "/api/")
	for _, p := range []string{"/totally/bogus", "/some/deep/unknown/path", "/login/bogus", "/bogus.txt"} {
		code, body := drive(t, h, http.MethodGet, p)
		if code != http.StatusNotFound {
			t.Errorf("%s: status %d (want 404)", p, code)
		}
		if strings.Contains(body, "landing") {
			t.Errorf("%s: served the landing page", p)
		}
	}
	if _, body := drive(t, h, http.MethodGet, "/totally/bogus"); !strings.Contains(body, "not found page") {
		t.Errorf("expected the export's 404.html, got %q", body)
	}
}

func TestHandler_DynamicRSCPayloadsResolveToPlaceholder(t *testing.T) {
	fsys := nextExportFS()
	fsys["projects/_.txt"] = &fstest.MapFile{Data: []byte("rsc project")}
	fsys["projects/_/__next._tree.txt"] = &fstest.MapFile{Data: []byte("rsc tree")}
	fsys["projects/_/settings.txt"] = &fstest.MapFile{Data: []byte("rsc settings")}
	h, _ := Handler(fsys, "/api/")
	cases := map[string]string{
		"/projects/tickero.txt":              "rsc project",
		"/projects/tickero/__next._tree.txt": "rsc tree",
		"/projects/tickero/settings.txt":     "rsc settings",
	}
	for path, want := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 200 || rr.Body.String() != want {
			t.Errorf("%s: %d %q (want 200 %q)", path, rr.Code, rr.Body.String(), want)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s: content-type %q", path, ct)
		}
	}
}

func TestHandler_APIPrefix404s(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, _ := drive(t, h, http.MethodGet, "/api/projects")
	if code != 404 {
		t.Errorf("api fallthrough: %d (want 404)", code)
	}
}

func TestHandler_HEADWorks(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	for _, p := range []string{"/", "/projects/new", "/projects/new/", "/_next/static/chunks/main.js"} {
		code, _ := drive(t, h, http.MethodHead, p)
		if code != 200 {
			t.Errorf("HEAD %s: %d", p, code)
		}
	}
}

func TestHandler_NonGETRejected(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	code, _ := drive(t, h, http.MethodPost, "/")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: %d (want 405)", code)
	}
}
