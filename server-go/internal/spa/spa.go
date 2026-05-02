// Package spa serves the embedded SPA bundle. Targets a Next.js static
// export (App Router with output: "export"), which lays files out as:
//
//   index.html
//   login.html         + login/        (the dir holds RSC .txt streams)
//   projects/new.html  + projects/new/ (same — both the html and a dir)
//   _next/static/...
//   _next/data/...
//
// So a request for /projects/new must serve projects/new.html. A request
// with a trailing slash (/projects/new/) must do the same — Next's
// own dev server treats those equivalently. Without the .html-sibling
// resolution, http.FileServer would try to serve the directory's index
// (which doesn't exist), 301 to a trailing-slash URL, and then 500.
//
// Asset requests (/_next/..., /favicon.ico, etc.) are served verbatim.
// Anything that doesn't match a file or a directory-with-html falls
// through to index.html so the App Router's client-side navigation
// keeps working for routes the export didn't pre-render.
package spa

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	pathpkg "path"
	"strings"
)

// Handler returns an http.Handler that serves the SPA from dist.
//
// apiPrefixes are paths that MUST NOT fall through to the SPA — when a
// request for one of them lands here we 404 instead of returning HTML.
// Without that guard a stale/typo'd /api/foo would render the SPA
// shell and break the client's error handling.
func Handler(dist fs.FS, apiPrefixes ...string) (http.Handler, error) {
	indexBytes, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// API/webhook routes never fall through to the SPA shell.
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				http.NotFound(w, r)
				return
			}
		}

		// Normalise leading + trailing slashes for embed.FS lookups.
		urlPath := strings.TrimPrefix(r.URL.Path, "/")
		urlPath = strings.TrimSuffix(urlPath, "/")
		if urlPath == "" {
			serveIndex(w, indexBytes)
			return
		}

		// 1. Direct file hit (e.g. /favicon.ico, /_next/static/x.js,
		//    /projects/new.html when the client asks for it explicitly).
		info, statErr := fs.Stat(dist, urlPath)
		if statErr == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		} else if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}

		// 2. Next-export pattern: /projects/new → projects/new.html,
		//    even though projects/new is also a directory holding the
		//    RSC streaming files. Try the .html sibling before falling
		//    through to index.
		if !strings.HasSuffix(urlPath, ".html") {
			candidate := urlPath + ".html"
			if info2, err2 := fs.Stat(dist, candidate); err2 == nil && !info2.IsDir() {
				serveStaticFile(w, dist, candidate)
				return
			}
		}

		// 3. Directory with index.html (rare in App Router exports but
		//    handle for completeness).
		if statErr == nil && info.IsDir() {
			candidate := pathpkg.Join(urlPath, "index.html")
			if info2, err2 := fs.Stat(dist, candidate); err2 == nil && !info2.IsDir() {
				serveStaticFile(w, dist, candidate)
				return
			}
		}

		// 4. SPA fallback — index.html serves the App Router shell so
		//    client-side navigation handles routes the export didn't
		//    pre-render (dynamic segments, deep links).
		serveIndex(w, indexBytes)
	}), nil
}

func serveIndex(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func serveStaticFile(w http.ResponseWriter, dist fs.FS, name string) {
	b, err := fs.ReadFile(dist, name)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// Embed is here only so the package is importable for its docstring;
// real consumers pass their own embed.FS.
var Embed = embed.FS{}
