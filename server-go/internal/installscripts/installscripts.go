// Package installscripts embeds the bash installers shipped with kuso
// so the running server can serve them at well-known URLs (no GitHub
// raw-CDN cache, no separate hosting). Two URLs to advertise:
//
//	https://<your-kuso>/install.sh        cluster bootstrapper
//	https://<your-kuso>/install-cli.sh    workstation CLI installer
//
// These are byte-for-byte copies of hack/install.sh + hack/install-cli.sh
// at build time. Go embed can't reach into ../../hack/ from this package,
// so the Dockerfile + Makefile copy them into ./scripts/ before
// go build. Editing scripts/*.sh directly is fine in a pinch but the
// canonical sources live in hack/.
//
// Why not serve them via the SPA's static dir? The Next.js export builds
// dist/ from web/, and stuffing shell scripts into web/public/ pollutes
// the SPA bundle and rebuild graph for every install.sh edit. Keeping
// the scripts in their own embed avoids that.
package installscripts

import (
	_ "embed"
	"fmt"
	"net/http"
)

//go:embed scripts/install.sh
var installSH []byte

//go:embed scripts/install-cli.sh
var installCliSH []byte

// InstallSH returns the cluster install script bytes.
func InstallSH() []byte { return installSH }

// InstallCliSH returns the CLI install script bytes.
func InstallCliSH() []byte { return installCliSH }

// Mount registers the two routes onto r. Both endpoints serve the
// scripts as text/x-shellscript with Cache-Control: no-store so a
// freshly-rolled server immediately serves the new version (no
// 5-minute raw-CDN delay biting the user during the install flow).
//
// The signature uses anything-with-Get because we need to mount on
// chi's router but don't want a chi import here — keeps the package
// reusable.
func Mount(get func(pattern string, h http.HandlerFunc)) {
	get("/install.sh", serve(installSH))
	get("/install-cli.sh", serve(installCliSH))
}

func serve(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		_, _ = w.Write(body)
	}
}
