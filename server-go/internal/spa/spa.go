// Package spa serves the embedded Vue SPA bundle. Requests for static
// asset paths are served verbatim from web/dist; everything else falls
// through to index.html so client-side routing keeps working.
//
// The bundle itself is embedded by the consuming caller via embed.FS —
// this package takes that FS and returns an http.Handler.
package spa

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

// Handler returns an http.Handler that serves the SPA from dist (an
// embed.FS sub-tree) with index.html fall-through.
//
// apiPrefixes are paths that MUST NOT fall through to the SPA — when a
// request for one of them lands here, we 404 instead of returning the
// SPA shell. Without that guard a stale/typo'd /api/foo would return
// HTML and break the Vue client's error handling.
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
		path := strings.TrimPrefix(r.URL.Path, "/")
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				http.NotFound(w, r)
				return
			}
		}
		// If the file exists, FileServer handles it. If not, fall back
		// to index.html so the SPA router can take over.
		if path != "" {
			if _, err := fs.Stat(dist, path); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			} else if !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	}), nil
}

// Embed is here only so the package is importable for its docstring;
// real consumers pass their own embed.FS.
var Embed = embed.FS{}
