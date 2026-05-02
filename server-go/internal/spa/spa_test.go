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
// RSC streaming payloads. This is the layout the spa Handler must
// resolve correctly.
func nextExportFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                                          {Data: []byte("<html>landing</html>")},
		"login.html":                                          {Data: []byte("<html>login</html>")},
		"login/__next._head.txt":                              {Data: []byte("rsc")},
		"projects/new.html":                                   {Data: []byte("<html>new project</html>")},
		"projects/new/__next._head.txt":                       {Data: []byte("rsc")},
		"projects/new/__next.!KGFwcCk.projects.new.txt":       {Data: []byte("rsc")},
		"_next/static/chunks/main.js":                         {Data: []byte("// js")},
		"favicon.ico":                                         {Data: []byte("ico")},
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

func TestHandler_UnknownRouteFallsBackToIndex(t *testing.T) {
	h, _ := Handler(nextExportFS(), "/api/")
	// Dynamic-segment routes that the export didn't pre-render: fall
	// back to index so client-side routing can take over.
	code, body := drive(t, h, http.MethodGet, "/projects/some-dynamic-id")
	if code != 200 {
		t.Fatalf("status: %d", code)
	}
	if !strings.Contains(body, "landing") {
		t.Errorf("body: %q", body)
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
