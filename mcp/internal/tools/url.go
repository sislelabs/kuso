// Shared URL-building helper for the tools package.
//
// Every tool builds its API paths through apiPath so user-supplied
// names (project, service, addon, env) are percent-escaped and can't
// break out of their path segment. Query strings go through
// url.Values at the call sites for the same reason.

package tools

import (
	"net/url"
	"strings"
)

// apiPath joins segments into a /-prefixed URL path, PathEscape-ing
// each segment. Fixed segments ("api", "projects") pass through
// unchanged; user-supplied ones get percent-encoded.
func apiPath(segments ...string) string {
	parts := make([]string, len(segments))
	for i, s := range segments {
		parts[i] = url.PathEscape(s)
	}
	return "/" + strings.Join(parts, "/")
}
