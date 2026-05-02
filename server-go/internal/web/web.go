// Package web embeds the SPA build output. It exists as a separate
// package so the Dockerfile can replace web/dist verbatim without
// touching any Go code.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// Dist returns the embedded SPA bundle rooted at web/dist.
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
