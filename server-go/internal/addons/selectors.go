package addons

import (
	"slices"
	"strings"
)

// AnnotationExcludedAddonConns lists, comma-separated, the project addon
// conn Secrets a named env deliberately does NOT mount: `env add` with
// per-env addons drops every project conn and mounts clones of only the
// requested kinds. Without the record, the next subscription pass saw a
// subscribed conn "missing" and re-added it — mounting PRODUCTION redis on
// an env created with `--addons postgres`.
const AnnotationExcludedAddonConns = "kuso.sislelabs.com/excluded-addon-conns"

// ExcludedAddonConns parses AnnotationExcludedAddonConns.
func ExcludedAddonConns(annotations map[string]string) []string {
	raw := annotations[AnnotationExcludedAddonConns]
	if raw == "" {
		return nil
	}
	var out []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// DropExcludedAddonConns removes the env's deliberately excluded conns.
func DropExcludedAddonConns(envFrom []string, annotations map[string]string) []string {
	excluded := ExcludedAddonConns(annotations)
	if len(excluded) == 0 {
		return envFrom
	}
	return slices.DeleteFunc(slices.Clone(envFrom), func(n string) bool { return slices.Contains(excluded, n) })
}
