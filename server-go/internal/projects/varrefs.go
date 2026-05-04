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
//   URL           → http://HOST:PORT (in-cluster; alias INTERNAL_URL)
//   INTERNAL_URL  → alias for URL (Railway parity)
//   PUBLIC_HOST   → first custom domain, or the auto domain on the
//                   production env (e.g. svc.proj.kuso.sislelabs.com).
//                   Empty when the service has no ingress (worker
//                   runtime, or env not provisioned yet).
//   PUBLIC_URL    → https://PUBLIC_HOST (or http:// when TLS is off
//                   on a custom domain). Empty when PUBLIC_HOST is
//                   empty — never falls back to the in-cluster URL.
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
// service-ref keys. Used by the resolver to choose between literal
// expansion and secretKeyRef. PUBLIC_* keys are included even though
// the resolver can return an empty string for them — that's the right
// signal for a worker / unprovisioned service, and we'd rather emit
// "" than fall through to the addon path and emit a broken
// secretKeyRef.
func IsServiceKey(key string) bool {
	switch key {
	case "HOST", "PORT", "URL", "INTERNAL_URL", "PUBLIC_HOST", "PUBLIC_URL":
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

// ServiceRef is everything the rewriter needs to expand any of the
// synthetic service keys. FQN+Port+NS cover in-cluster (HOST/PORT/
// URL/INTERNAL_URL); PublicHost+PublicTLS cover the public surface
// (PUBLIC_HOST/PUBLIC_URL). PublicHost is empty for services with no
// ingress (worker runtimes, or services whose production env hasn't
// been created yet) — callers should treat that as "no public URL"
// rather than falling back to the in-cluster URL.
type ServiceRef struct {
	FQN        string
	Port       int32
	NS         string
	PublicHost string
	PublicTLS  bool
}

// ServiceRefResolver looks up service connection details by short
// (or FQ) name. ok=false when the name doesn't match a service in
// the project. Supplied by SetEnv so the rewriter doesn't have to
// import kube/projects state.
type ServiceRefResolver func(name string) (ServiceRef, bool)

// AddonRefResolver returns the conn-secret name for an addon (short
// or fqn form), or ok=false when no such addon exists in the project.
// Without this, the rewriter would happily emit a secretKeyRef for
// a non-existent addon → pod crashloops on missing secret mount.
type AddonRefResolver func(name string) (connSecretName string, ok bool)

// ExpandServiceKey turns a (ServiceRef, key) pair into the literal
// value that goes on the pod env. Mirrors Railway's reference-
// variable surface plus the kuso PUBLIC_* extension.
func ExpandServiceKey(ref ServiceRef, key string) string {
	host := ref.FQN + "." + ref.NS + ".svc.cluster.local"
	switch key {
	case "HOST":
		return host
	case "PORT":
		if ref.Port == 0 {
			return ""
		}
		return fmt.Sprintf("%d", ref.Port)
	case "URL", "INTERNAL_URL":
		if ref.Port == 0 {
			return "http://" + host
		}
		return fmt.Sprintf("http://%s:%d", host, ref.Port)
	case "PUBLIC_HOST":
		return ref.PublicHost
	case "PUBLIC_URL":
		// Empty PublicHost = no ingress; emit empty rather than
		// silently falling back to the in-cluster URL (which a
		// browser-side caller cannot reach and would mask the bug).
		if ref.PublicHost == "" {
			return ""
		}
		scheme := "https"
		if !ref.PublicTLS {
			scheme = "http"
		}
		return scheme + "://" + ref.PublicHost
	}
	return ""
}

// ErrUnknownVarRef is returned when a ${{ name.KEY }} ref doesn't
// match any known service or addon in the project. Without this,
// SetEnv would silently emit a secretKeyRef pointing at a Secret
// that doesn't exist and the pod would crashloop on mount.
var ErrUnknownVarRef = errors.New("variable reference does not match any service or addon in this project")

// RewriteEnvVar maps a wire-shape EnvVar through the var-ref parser.
//
// Resolution order when the value is a pure `${{ name.KEY }}` ref:
//   1. If KEY is a service-ref key (HOST/PORT/URL/INTERNAL_URL) AND
//      svcResolver finds <name> as a service in this project, expand
//      to a literal Value (DNS / port / url string).
//   2. Else if addonResolver finds <name> as an addon, emit a
//      secretKeyRef pointing at <addon>-conn / KEY.
//   3. Else: ErrUnknownVarRef. We refuse to silently write a broken
//      reference.
//
// When the resolvers are nil, all refs fall through to the legacy
// addon path (no validation). Callers that supply both resolvers
// get full validation; callers that supply neither preserve the
// pre-v0.6.16 behaviour.
func RewriteEnvVar(in EnvVar, svcResolver ServiceRefResolver, addonResolver AddonRefResolver) (EnvVar, error) {
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
		if sref, ok := svcResolver(ref.Name); ok {
			expanded := ExpandServiceKey(sref, ref.Key)
			return EnvVar{Name: in.Name, Value: expanded}, nil
		}
	}
	// Addon-ref path: emit a secretKeyRef IFF the addon exists.
	if addonResolver != nil {
		if connSecret, ok := addonResolver(ref.Name); ok {
			return EnvVar{
				Name: in.Name,
				ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{
						"name": connSecret,
						"key":  ref.Key,
					},
				},
			}, nil
		}
		// Resolver supplied + neither a service nor an addon matched
		// → refuse. This is the new strictness.
		return EnvVar{}, fmt.Errorf("env var %q: %w (looked up %q)", in.Name, ErrUnknownVarRef, ref.Name)
	}
	// No addonResolver wired → legacy behaviour: trust the ref name
	// and emit a secretKeyRef. Preserves callers that haven't been
	// updated. New code should always pass an addonResolver.
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
func RewriteEnvVars(in []EnvVar, svcResolver ServiceRefResolver, addonResolver AddonRefResolver) ([]EnvVar, error) {
	out := make([]EnvVar, 0, len(in))
	for _, v := range in {
		rewritten, err := RewriteEnvVar(v, svcResolver, addonResolver)
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
