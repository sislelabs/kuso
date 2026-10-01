// Package spa serves the embedded SPA bundle. Targets a Next.js static
// export (App Router with output: "export"), which lays files out as:
//
//	index.html
//	login.html         + login/        (the dir holds RSC .txt streams)
//	projects/new.html  + projects/new/ (same — both the html and a dir)
//	_next/static/...
//	_next/data/...
//
// So a request for /projects/new must serve projects/new.html. A request
// with a trailing slash (/projects/new/) must do the same — Next's
// own dev server treats those equivalently. Without the .html-sibling
// resolution, http.FileServer would try to serve the directory's index
// (which doesn't exist), 301 to a trailing-slash URL, and then 500.
//
// Asset requests (/_next/..., /favicon.ico, etc.) are served verbatim.
// Dynamic routes resolve to the export's "_" placeholder; anything else
// gets the export's 404 page with a 404 status.
package spa

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	pathpkg "path"
	"strings"

	"kuso/server/internal/httperr"
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
		// API/webhook routes never fall through to the SPA shell. Checked
		// before the method so a typo'd POST /api/... is a 404, not a 405.
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				httperr.Write(w, http.StatusNotFound, "no API route "+r.Method+" "+r.URL.Path)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httperr.Write(w, http.StatusMethodNotAllowed, "method "+r.Method+" not allowed on "+r.URL.Path)
			return
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

		// 4. Dynamic-segment fallback. Next's static export emits a
		//    "_.html" sibling for every dynamic route — for /projects/[project]
		//    that's projects/_.html, for /projects/[project]/services/[service]
		//    that's projects/_/services/_.html. Walk up from the deepest
		//    parent looking for a directory whose sibling _.html exists,
		//    substituting "_" for the unknown leaf segment(s).
		//
		//    Without this, /projects/kuso-hello-go fell through to the
		//    root index.html (the marketing landing) which then bounced
		//    authenticated users back to /projects.
		//
		//    RSC payloads (`.txt`) for a dynamic route resolve the same
		//    way to the placeholder's payload. Answering those with HTML
		//    made the client router fall back to a full page load on
		//    every project/service navigation.
		if strings.HasSuffix(urlPath, ".txt") {
			if txt := dynamicPayloadFallback(dist, urlPath); txt != "" {
				serveStaticFile(w, dist, txt)
				return
			}
			http.NotFound(w, r)
			return
		}
		if html := dynamicFallback(dist, urlPath); html != "" {
			serveStaticFile(w, dist, html)
			return
		}

		// 5. Nothing matches: a real 404 (the export's not-found page),
		//    not the marketing landing with 200.
		serveNotFound(w, dist)
	}), nil
}

// resolveDynamicDirs maps each segment of a directory path to its
// literal name when that directory exists in dist, else to the "_"
// placeholder Next emits for dynamic segments.
func resolveDynamicDirs(dist fs.FS, parts []string) []string {
	resolved := make([]string, len(parts))
	prefix := ""
	for i, seg := range parts {
		trial := seg
		if prefix != "" {
			trial = prefix + "/" + seg
		}
		if info, err := fs.Stat(dist, trial); err == nil && info.IsDir() {
			resolved[i] = seg
		} else {
			resolved[i] = "_"
		}
		prefix = strings.Join(resolved[:i+1], "/")
	}
	return resolved
}

// dynamicPayloadFallback resolves an RSC payload request under a
// dynamic route to the placeholder's payload:
//
//	projects/tickero.txt                  → projects/_.txt
//	projects/tickero/__next._tree.txt     → projects/_/__next._tree.txt
//	projects/tickero/settings.txt         → projects/_/settings.txt
func dynamicPayloadFallback(dist fs.FS, urlPath string) string {
	parts := strings.Split(urlPath, "/")
	dirs := resolveDynamicDirs(dist, parts[:len(parts)-1])
	leaf := parts[len(parts)-1]
	base := strings.Join(dirs, "/")
	join := func(name string) string {
		if base == "" {
			return name
		}
		return base + "/" + name
	}
	for _, name := range []string{leaf, "_.txt"} {
		candidate := join(name)
		if info, err := fs.Stat(dist, candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func serveNotFound(w http.ResponseWriter, dist fs.FS) {
	for _, name := range []string{"404.html", "_not-found.html"} {
		b, err := fs.ReadFile(dist, name)
		if err != nil {
			continue
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		applyHTMLSecurityHeaders(w)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(b)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// dynamicFallback resolves an unknown URL path to the deepest matching
// Next-export "_.html" placeholder. For each segment of urlPath, it
// keeps the segment literal if a directory by that name exists in
// dist; otherwise it substitutes "_". After walking the full path it
// looks for <resolved>.html, then climbs upward (truncating one
// resolved segment at a time) until it finds a "_.html" or runs out.
//
// Examples (with FS containing projects/_.html and projects/_/services/_.html):
//
//	/projects/kuso-hello-go      → projects/_.html
//	/projects/abc/services/web   → projects/_/services/_.html
//	/projects/abc/unknown-leaf   → projects/_.html (climbs up since
//	                               projects/_/unknown-leaf.html isn't there)
//
// Empty string when no fallback exists; caller falls back to the
// root index.html.
func dynamicFallback(dist fs.FS, urlPath string) string {
	parts := strings.Split(urlPath, "/")
	resolved := make([]string, len(parts))
	prefix := ""
	for i, seg := range parts {
		// Look for a literal directory at this depth. If the literal
		// directory exists OR a literal file with this name exists,
		// keep the segment as-is. Otherwise it's a dynamic param and
		// becomes "_".
		var trial string
		if prefix == "" {
			trial = seg
		} else {
			trial = prefix + "/" + seg
		}
		if info, err := fs.Stat(dist, trial); err == nil && info.IsDir() {
			resolved[i] = seg
		} else if info, err := fs.Stat(dist, trial+".html"); err == nil && !info.IsDir() {
			// Static leaf hit — caller would have served this above,
			// but include for safety: keep literal so the eventual
			// .html lookup matches.
			resolved[i] = seg
		} else {
			resolved[i] = "_"
		}
		prefix = strings.Join(resolved[:i+1], "/")
	}
	// Try the deepest candidate first, then climb up replacing the
	// trailing N segments with "_". This handles "/projects/abc/missing-leaf"
	// where projects/_/missing-leaf.html doesn't exist but projects/_.html does.
	for depth := len(resolved); depth >= 1; depth-- {
		// Only climb to a dynamic placeholder; climbing to a static
		// page would render e.g. /settings for /settings/bogus.
		if depth < len(resolved) && resolved[depth-1] != "_" {
			continue
		}
		candidate := strings.Join(resolved[:depth], "/") + ".html"
		if info, err := fs.Stat(dist, candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// htmlSecurityHeaders are applied to every HTML response served by
// the SPA handler. Static assets (JS/CSS/fonts/images) inherit the
// caller's defaults — they don't need framing/clickjack guards
// because they're not the entry point for a user session.
//
// CSP is intentionally permissive on script-src ('self' + 'unsafe-inline'
// + 'unsafe-eval') because Next's static export inlines a runtime
// hydration shim and uses eval-like patterns in dev. The realistic
// threat we close is "XSS in a third-party script-injection point" —
// frame-ancestors + X-Frame-Options + nosniff are the load-bearing
// pieces. HSTS lets browsers keep the connection HTTPS even if the
// user types `http://`. base-uri 'self' blocks <base> rewrites.
//
// connect-src is open ('self' + websocket schemes for log streaming);
// img-src + style-src 'unsafe-inline' covers the design system.
var htmlSecurityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https:; " +
		"font-src 'self' data:; " +
		"connect-src 'self' ws: wss:; " +
		"frame-ancestors 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'",
	"X-Frame-Options":           "DENY",
	"X-Content-Type-Options":    "nosniff",
	"Referrer-Policy":           "strict-origin-when-cross-origin",
	"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	"Permissions-Policy":        "geolocation=(), microphone=(), camera=()",
}

func applyHTMLSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	for k, v := range htmlSecurityHeaders {
		h.Set(k, v)
	}
}

func serveIndex(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	applyHTMLSecurityHeaders(w)
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
		applyHTMLSecurityHeaders(w)
	} else if strings.HasSuffix(name, ".txt") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// Embed is here only so the package is importable for its docstring;
// real consumers pass their own embed.FS.
var Embed = embed.FS{}
