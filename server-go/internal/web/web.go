// Package web embeds the SPA build output. It exists as a separate
// package so the Dockerfile can replace dist/ verbatim without touching
// any Go code.
//
// During the Vue→Next.js rewrite (see docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md),
// both the legacy Vue dist and the new Next.js dist-next coexist on disk
// and are baked into the binary. KUSO_FRONTEND=next switches at runtime
// to the new bundle. Once Phase F lands, the legacy embed is removed
// and dist-next is renamed back to dist.
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed dist
var distLegacyFS embed.FS

//go:embed dist-next
var distNextFS embed.FS

// Dist returns the embedded SPA bundle. Defaults to the legacy Vue dist.
// Set KUSO_FRONTEND=next to serve the Next.js build instead.
func Dist() (fs.FS, error) {
	if os.Getenv("KUSO_FRONTEND") == "next" {
		return fs.Sub(distNextFS, "dist-next")
	}
	return fs.Sub(distLegacyFS, "dist")
}
