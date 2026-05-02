// Package web embeds the SPA build output. It exists as a separate
// package so the Dockerfile can replace dist/ verbatim without touching
// any Go code.
//
// During the Vue→Next.js rewrite (see docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md),
// both the legacy Vue dist and the new Next.js dist-next coexist on disk
// and are baked into the binary. After Phase F, the Next.js bundle is
// the default. KUSO_FRONTEND=legacy serves the Vue dist as a rollback
// option while the rewrite is bedding in; this flag is removed when
// the legacy dist is deleted.
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed all:dist
var distLegacyFS embed.FS

//go:embed all:dist-next
var distNextFS embed.FS

// Dist returns the embedded SPA bundle. Defaults to the Next.js bundle
// (post-Phase F). Set KUSO_FRONTEND=legacy to serve the Vue dist as a
// rollback during the cutover window.
func Dist() (fs.FS, error) {
	if os.Getenv("KUSO_FRONTEND") == "legacy" {
		return fs.Sub(distLegacyFS, "dist")
	}
	return fs.Sub(distNextFS, "dist-next")
}
