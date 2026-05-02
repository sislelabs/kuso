// Package version exposes the build-time kuso-server version string.
//
// The VERSION file at the module root is the source of truth and is also
// stamped into the container image label by Dockerfile.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Version returns the trimmed contents of VERSION.
func Version() string {
	return strings.TrimSpace(raw)
}
