// Package web embeds the SPA build output. It exists as a separate
// package so the Dockerfile can replace dist/ verbatim without touching
// any Go code.
//
// The bundle is the Next.js 16 static export from ../web/. See
// docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md
// for the rewrite design and scripts/build-frontend.sh for the build
// pipeline.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the embedded SPA bundle.
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
