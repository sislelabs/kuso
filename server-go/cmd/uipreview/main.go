// uipreview serves a local web/out export through the real SPA fallback and
// proxies read-only (GET) API traffic to a live instance. Dev-only harness
// for UI review rounds; it never forwards mutating requests.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"kuso/server/internal/spa"
)

func main() {
	dist := flag.String("dist", "../web/out", "next export dir")
	upstream := flag.String("upstream", "https://kuso.sislelabs.com", "live API base")
	addr := flag.String("addr", ":3100", "listen addr")
	allowWrites := flag.Bool("allow-writes", false, "forward non-GET requests (sandbox only)")
	flag.Parse()

	u, err := url.Parse(*upstream)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)
		r.Host = u.Host
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}

	spaH, err := spa.Handler(os.DirFS(*dist), "/api/", "/ws/", "/healthz")
	if err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			if r.Method != http.MethodGet && !*allowWrites {
				http.Error(w, "uipreview is read-only", http.StatusForbidden)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
		spaH.ServeHTTP(w, r)
	})
	log.Printf("uipreview on %s → %s", *addr, *upstream)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
