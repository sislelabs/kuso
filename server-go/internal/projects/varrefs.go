package projects

import (
	"context"
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
//   HOST          → <fqn>-production.<namespace>.svc.cluster.local
//                   (the kusoenvironment helm chart names the Service
//                   after the release, which is <fqn>-production. The
//                   *bare* <fqn> is the KusoService CR name, not a
//                   kube Service — no DNS for it.)
//   PORT          → 80 (the kube Service's port; the chart maps it to
//                   the env's containerPort internally)
//   URL           → http://HOST (in-cluster; alias INTERNAL_URL).
//                   Port elided because it's always 80; explicit :80
//                   in URLs trips up some HTTP libraries.
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
	// EnvScope is the env-group short name of the env doing the
	// referencing (production, staging, preview-pr-N). Empty defaults
	// to "production" for pre-v0.17.1 callers. Used by ExpandServiceKey
	// to land in-cluster HOST/URL/INTERNAL_URL on the matching env's
	// kube Service: a staging service that says ${{ api.URL }} resolves
	// to <project>-api-staging.<ns>.svc.cluster.local, not the
	// production sibling (which used to be the hardcoded behavior and
	// silently leaked staging traffic to production — B4.1/B4.2 from
	// the v0.17.0 audit).
	EnvScope string
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
//
// Why `<fqn>-production`: the kusoenvironment helm chart names every
// kube object after the release, which is productionEnvName(...) =
// "<project>-<service>-production". ref.FQN is the KusoService CR
// name (i.e. "<project>-<service>"), so we suffix "-production" to
// land on the actual Service.
//
// Why port 80: the chart's service.yaml fixes Service.spec.ports[0].
// port to 80 and maps it to the named "http" targetPort (which is
// ref.Port). External callers always hit 80; the containerPort is
// only relevant inside the pod.
//
// Previews use their own env scope (preview-pr-N) and would need a
// separate expansion path — they currently don't get one because
// PUBLIC_URL is what previews want anyway. Until that lands, refs
// from a preview env to a sibling preview pod resolve to the
// production sibling, which is the safer default (matches "preview
// reads against prod data" expectation).
func ExpandServiceKey(ref ServiceRef, key string) string {
	envScope := ref.EnvScope
	if envScope == "" {
		envScope = "production"
	}
	host := ref.FQN + "-" + envScope + "." + ref.NS + ".svc.cluster.local"
	switch key {
	case "HOST":
		return host
	case "PORT":
		// Service port is always 80 — see comment above.
		return "80"
	case "URL", "INTERNAL_URL":
		// Elide :80 from the URL. Most HTTP clients handle the
		// explicit port fine, but Go's http2 transport and some
		// SDKs (notably older `requests` releases) misroute
		// host:80 against a non-TLS scheme on retry. Leaving it
		// implicit is the safer wire format.
		return "http://" + host
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

// RewriteOpts controls how RewriteEnvVar handles refs that don't yet
// resolve to a known addon. The default (zero value) is strict:
// unknown ref → ErrUnknownVarRef. The SPA's env editor sets
// AllowPending=true so a user can save an env var that references an
// addon still mid-provisioning; the secret mount resolves itself once
// the addon's `<addon>-conn` Secret materialises and the pod
// restarts.
type RewriteOpts struct {
	// AllowPending lets unknown addon refs through with a speculative
	// secretKeyRef pointing at <name>-conn (or <project>-<name>-conn
	// for short refs). The kube secret-mount machinery handles
	// eventual consistency: when the secret appears, the next pod
	// restart picks up the new value. Until then the pod stays in
	// CreateContainerConfigError, which the UI surfaces as
	// "addon pending."
	AllowPending bool
}

// RewriteEnvVar maps a wire-shape EnvVar through the var-ref parser.
//
// Resolution order when the value is a pure `${{ name.KEY }}` ref:
//  1. If KEY is a service-ref key (HOST/PORT/URL/INTERNAL_URL) AND
//     svcResolver finds <name> as a service in this project, expand
//     to a literal Value (DNS / port / url string).
//  2. Else if addonResolver finds <name> as an addon, emit a
//     secretKeyRef pointing at <addon>-conn / KEY.
//  3. Else if opts.AllowPending: emit a speculative secretKeyRef
//     that resolves once the addon's conn Secret exists.
//  4. Else: ErrUnknownVarRef. We refuse to silently write a broken
//     reference.
//
// When the resolvers are nil, all refs fall through to the
// unvalidated addon path. Production wires real resolvers; the
// nil path exists for the var-ref parser tests in varrefs_test.go.
func RewriteEnvVar(in EnvVar, svcResolver ServiceRefResolver, addonResolver AddonRefResolver) (EnvVar, error) {
	return RewriteEnvVarWithOpts(in, svcResolver, addonResolver, RewriteOpts{})
}

// RewriteEnvVarWithOpts is the variant with explicit options. New
// callers should pass through user intent (allowPending=true for
// interactive editor saves; false for kuso.yml apply).
func RewriteEnvVarWithOpts(in EnvVar, svcResolver ServiceRefResolver, addonResolver AddonRefResolver, opts RewriteOpts) (EnvVar, error) {
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
		// Resolver supplied + neither a service nor an addon matched.
		// In strict mode we refuse; in pending mode we emit a
		// speculative secretKeyRef using the canonical naming so the
		// pod resolves the value once the addon's conn Secret lands.
		// Kube's CreateContainerConfigError surfaces the missing
		// secret in the UI as "addon pending" until then.
		if opts.AllowPending {
			// The KusoService / KusoEnvironment CRDs lock
			// secretKeyRef.name to ^[a-z0-9][a-z0-9-]*-conn$ — kube
			// admission rejects anything else outright. The
			// var-ref parser accepts [A-Za-z0-9_-] for backwards
			// compat, so an uppercase or underscore name would
			// reach this branch and then get bounced by kube. Catch
			// it here with a clearer error so the user fixes the
			// ref instead of seeing an opaque admission failure.
			if !addonRefDNSSafe(ref.Name) {
				return EnvVar{}, fmt.Errorf("env var %q: addon ref %q must be lowercase letters/digits/dashes for pending-mode resolution", in.Name, ref.Name)
			}
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
		return EnvVar{}, fmt.Errorf("env var %q: %w (looked up %q)", in.Name, ErrUnknownVarRef, ref.Name)
	}
	// No addonResolver wired → trust the ref name and emit a
	// secretKeyRef without lookup. Test-only path; production
	// always wires a resolver.
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
	return RewriteEnvVarsWithOpts(in, svcResolver, addonResolver, RewriteOpts{})
}

// RewriteEnvVarsWithOpts is the variant that threads RewriteOpts
// through to each var so the caller (interactive editor vs. yaml
// apply) can pick the strictness.
func RewriteEnvVarsWithOpts(in []EnvVar, svcResolver ServiceRefResolver, addonResolver AddonRefResolver, opts RewriteOpts) ([]EnvVar, error) {
	out := make([]EnvVar, 0, len(in))
	for _, v := range in {
		rewritten, err := RewriteEnvVarWithOpts(v, svcResolver, addonResolver, opts)
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

// addonRefDNSSafe matches the CRD pattern enforced on
// secretKeyRef.name (^[a-z0-9][a-z0-9-]*-conn$) ignoring the trailing
// `-conn` suffix that SecretName() appends. Used by the AllowPending
// path to reject refs the CRD admission would bounce.
var addonRefDNSSafeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func addonRefDNSSafe(name string) bool {
	return addonRefDNSSafeRE.MatchString(name)
}

// validateSecretRefName enforces that a client-supplied
// secretKeyRef.name points at a Secret THIS project legitimately owns,
// closing the cross-project credential-theft vector: in the default
// single-namespace install every project's `-conn`/`-shared` secrets
// live side by side, and the CRD admission regex only checks the shape
// (`*-conn` / `*-shared`), not ownership. Without this an editor on
// project A could set an env var referencing `projb-pg-conn` and read
// project B's DB password at pod runtime.
//
// Allowed for project P (service SVC):
//   - any of P's addon conn secrets (via AddonConnSecrets)
//   - "<P>-shared"                  (project shared secrets)
//   - "<P>-<SVC>-secrets"           (this service's own secret)
//   - "kuso-instance-shared"        (instance-wide shared secrets)
//
// Returns ErrInvalid for anything else. Fails safe: if the addon
// resolver is unwired (tests) the deterministic names above still pass,
// but a foreign `-conn` name is rejected.
func (s *Service) validateSecretRefName(ctx context.Context, project, service, name string) error {
	// Resolve the owned addon-conn set on demand. Callers validating a
	// batch of refs should prefer ownedAddonConnSet + validateSecretRefNameIn
	// to avoid one full kube LIST per ref (see validateAndRewriteEnvVars).
	owned, err := s.ownedAddonConnSet(ctx, project)
	if err != nil {
		return err
	}
	return s.validateSecretRefNameIn(project, service, name, owned)
}

// ownedAddonConnSet returns the project's addon connection-secret names
// as a set, resolved from AddonConnSecrets with ONE kube LIST. Callers
// validating many refs resolve this once and hand it to
// validateSecretRefNameIn, avoiding an N+1 LIST fan-out. Returns an empty
// (non-nil) set when AddonConnSecrets isn't wired.
func (s *Service) ownedAddonConnSet(ctx context.Context, project string) (map[string]struct{}, error) {
	set := map[string]struct{}{}
	if s.AddonConnSecrets == nil {
		return set, nil
	}
	owned, err := s.AddonConnSecrets(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("resolve addon secrets: %w", err)
	}
	for _, sec := range owned {
		set[sec] = struct{}{}
	}
	return set, nil
}

// validateSecretRefNameIn is validateSecretRefName against a pre-resolved
// owned addon-conn set — no kube LIST. Same acceptance rules.
func (s *Service) validateSecretRefNameIn(project, service, name string, ownedAddonConn map[string]struct{}) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: secretRef.name required", ErrInvalid)
	}
	if name == instanceSharedSecretName || name == project+"-shared" {
		return nil
	}
	// This service's own secret(s). Accept the service secret
	// (<P>-<SVC>-secrets) AND any env-scoped secret
	// (<P>-<SVC>-<env>-secrets, e.g. <P>-<SVC>-staging-secrets) — both are
	// owned by this service. We match by the <P>-<SVC>- prefix + -secrets
	// suffix rather than enumerating env names, since the env segment is a
	// slugified free-form scope (see kube.EnvSecretName). Accept both the
	// FQN and short service-name forms callers pass.
	svcShort := strings.TrimPrefix(service, project+"-")
	svcSecretPrefix := project + "-" + svcShort + "-"
	if name == project+"-"+svcShort+"-secrets" ||
		(strings.HasPrefix(name, svcSecretPrefix) && strings.HasSuffix(name, "-secrets")) {
		return nil
	}
	if _, ok := ownedAddonConn[name]; ok {
		return nil
	}
	return fmt.Errorf("%w: secretRef.name %q is not a secret owned by project %q", ErrInvalid, name, project)
}

// instanceSharedSecretName is the instance-wide shared secret every
// project may reference. Kept as a const so the validator and the
// existing ref-rewrite paths agree on the exact name.
const instanceSharedSecretName = "kuso-instance-shared"

// secretRefNameOf extracts (name, key) from a valueFrom map shaped
// {"secretKeyRef": {"name": ..., "key": ...}}. Returns ("", "") when the
// map isn't a secretKeyRef (e.g. a configMapKeyRef or a malformed entry).
func secretRefNameOf(valueFrom map[string]any) (name, key string) {
	if valueFrom == nil {
		return "", ""
	}
	skrRaw, ok := valueFrom["secretKeyRef"]
	if !ok {
		return "", ""
	}
	skr, ok := skrRaw.(map[string]any)
	if !ok {
		return "", ""
	}
	name, _ = skr["name"].(string)
	key, _ = skr["key"].(string)
	return name, key
}
