package projects

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// VarRef parses a `${{ <name>.<KEY> }}` reference. <name> can be either
// an addon (resolves to a secretKeyRef on the addon's conn secret) or
// another service in the same project (resolves to a literal value
// pointing at the service's in-cluster DNS — Railway's "reference
// variables" pattern). The whole-string form rewrites the env var.
// Composite forms (anything around the reference) are rejected because
// we want zero runtime templating — kube does the resolution via
// envFrom/valueFrom or just stores the literal expansion.
//
// Service refs support these synthetic keys:
//   HOST          → <service-fqn>.<namespace>.svc.cluster.local
//   PORT          → spec.port (string)
//   URL           → http://HOST:PORT
//   INTERNAL_URL  → alias for URL (Railway parity)
//
// The resolver decides addon-vs-service at rewrite time using the
// project's known names. When neither matches we leave the value
// literal — useful for refs to addons/services that haven't been
// created yet (the env var resolves the next time someone touches it).

// ErrCompositeVarRef is returned when an env var value mixes literals
// with a `${{ ... }}` reference. The user must pick one or the other.
var ErrCompositeVarRef = errors.New("variable references must be the entire value")

// VarRef is the parsed shape of a pure `${{ <name>.<KEY> }}` value.
// Whether <name> is an addon or a service is decided downstream by the
// resolver — the parser only sees the literal text.
type VarRef struct {
	Name string // addon or service short name
	Key  string
}

// SecretName returns the kube Secret name where an addon's connection
// lives. Only meaningful when this VarRef resolves to an addon —
// addons.ConnSecretName is the canonical version. Kept as a method for
// the existing callers that built secretKeyRef entries inline.
func (r VarRef) SecretName() string {
	return r.Name + "-conn"
}

// IsServiceKey reports whether the key is one of the synthetic
// service-ref keys (HOST/PORT/URL/INTERNAL_URL). Used by the resolver
// to choose between literal expansion and secretKeyRef.
func IsServiceKey(key string) bool {
	switch key {
	case "HOST", "PORT", "URL", "INTERNAL_URL":
		return true
	}
	return false
}

// pure-form regex: ^\$\{\{\s*<name>\.<KEY>\s*\}\}$
var varRefRe = regexp.MustCompile(`^\$\{\{\s*([a-zA-Z0-9_-]+)\.([A-Z_][A-Z0-9_]*)\s*\}\}$`)

// composite-form regex: any `${{ ... }}` inside but with surrounding
// content. Used to distinguish "valid pure ref" from "user typo".
var anyVarRefRe = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// ParseVarRef returns (ref, true, nil) for a pure reference,
// (zero, false, nil) for a literal value with no reference at all,
// and (zero, false, ErrCompositeVarRef) for any composite form.
func ParseVarRef(value string) (VarRef, bool, error) {
	m := varRefRe.FindStringSubmatch(value)
	if m != nil {
		return VarRef{Name: m[1], Key: m[2]}, true, nil
	}
	if anyVarRefRe.MatchString(value) {
		return VarRef{}, false, ErrCompositeVarRef
	}
	return VarRef{}, false, nil
}

// ServiceRefResolver looks up service connection details by short
// name. Returns the FQN service name + port, or ok=false when the
// service doesn't exist. The resolver is supplied by SetEnv so the
// rewriter doesn't have to import kube/projects state.
type ServiceRefResolver func(shortName string) (fqn string, port int32, ns string, ok bool)

// ExpandServiceKey turns a (service, key) pair into the literal value
// that goes on the pod env. Mirrors Railway's reference-variable
// surface: HOST/PORT/URL/INTERNAL_URL.
func ExpandServiceKey(fqn string, port int32, ns, key string) string {
	host := fqn + "." + ns + ".svc.cluster.local"
	switch key {
	case "HOST":
		return host
	case "PORT":
		if port == 0 {
			return ""
		}
		return fmt.Sprintf("%d", port)
	case "URL", "INTERNAL_URL":
		if port == 0 {
			return "http://" + host
		}
		return fmt.Sprintf("http://%s:%d", host, port)
	}
	return ""
}

// RewriteEnvVar maps a wire-shape EnvVar through the var-ref parser.
//
// Resolution order when the value is a pure `${{ name.KEY }}` ref:
//   1. If KEY is a service-ref key (HOST/PORT/URL/INTERNAL_URL) AND
//      svcResolver finds <name> as a service in this project, expand
//      to a literal Value (DNS / port / url string).
//   2. Otherwise treat <name> as an addon and emit a secretKeyRef
//      pointing at <name>-conn / KEY (the legacy behaviour).
//
// When svcResolver is nil, every ref falls through to the addon path —
// preserves SetEnv backwards compat for callers that haven't been
// updated.
func RewriteEnvVar(in EnvVar, svcResolver ServiceRefResolver) (EnvVar, error) {
	// Pre-existing valueFrom entries pass through. Only literal `value`
	// entries are candidates for rewriting.
	if in.ValueFrom != nil || in.Value == "" {
		return in, nil
	}
	ref, ok, err := ParseVarRef(in.Value)
	if err != nil {
		return EnvVar{}, fmt.Errorf("env var %q: %w", in.Name, err)
	}
	if !ok {
		return in, nil
	}
	// Service-ref path: synthetic key + name resolves to a service.
	if IsServiceKey(ref.Key) && svcResolver != nil {
		if fqn, port, ns, ok := svcResolver(ref.Name); ok {
			expanded := ExpandServiceKey(fqn, port, ns, ref.Key)
			return EnvVar{Name: in.Name, Value: expanded}, nil
		}
	}
	// Default: secretKeyRef on the addon's conn-secret.
	return EnvVar{
		Name: in.Name,
		ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{
				"name": ref.SecretName(),
				"key":  ref.Key,
			},
		},
	}, nil
}

// RewriteEnvVars applies RewriteEnvVar over a slice. Returns the first
// error encountered, or the rewritten slice.
func RewriteEnvVars(in []EnvVar, svcResolver ServiceRefResolver) ([]EnvVar, error) {
	out := make([]EnvVar, 0, len(in))
	for _, v := range in {
		rewritten, err := RewriteEnvVar(v, svcResolver)
		if err != nil {
			return nil, err
		}
		out = append(out, rewritten)
	}
	return out, nil
}

// FormatVarRef returns the canonical wire form for a (name, key) pair.
// Used by the frontend autocomplete to insert the right syntax.
func FormatVarRef(name, key string) string {
	return "${{ " + strings.TrimSpace(name) + "." + strings.TrimSpace(key) + " }}"
}
