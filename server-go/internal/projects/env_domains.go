// Per-env custom-domain operations. The KusoEnvironment.spec.host +
// AdditionalHosts pair is what the chart's Ingress reads, so these
// edits go straight to the env CR — no service-level propagation.
//
// Why per-env instead of service-level:
//   - Production and staging serve different hostnames
//     (tickero.bg vs staging.tickero.bg). The service-level
//     spec.domains used to mirror to every env's additionalHosts on
//     every save, so adding tickero.bg in the production tab would
//     make staging start claiming it too → Ingress conflict.
//   - The Networking section in the dashboard binds to the env CR's
//     host + additionalHosts directly; CLI exposes
//     `kuso env add-domain / remove-domain --env <name>` for parity.

package projects

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"kuso/server/internal/kube"
)

// AddEnvDomain appends a custom host to env.Spec.AdditionalHosts.
// Same dedupe + TLS-eligibility checks as the service-level AddDomain.
// envName is the user-facing short env name (production, staging,
// preview-pr-N); we resolve to the FQ CR name internally.
//
// Wildcard hosts ("*.example.com") require tlsSecret — the name of a
// pre-provisioned TLS secret (a DNS-01 wildcard cert; Let's Encrypt
// can't HTTP-01 a wildcard). They land in env.Spec.WildcardDomains,
// never in AdditionalHosts/TLSHosts, so no per-host certs are minted
// for covered subdomains.
func (s *Service) AddEnvDomain(ctx context.Context, project, service, envName, host, tlsSecret string) (*kube.KusoEnvironment, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	tlsSecret = strings.TrimSpace(tlsSecret)
	if host == "" {
		return nil, fmt.Errorf("%w: host required", ErrInvalid)
	}
	wildcard := strings.HasPrefix(host, "*.")
	if wildcard {
		if !validHostname(strings.TrimPrefix(host, "*.")) {
			return nil, fmt.Errorf("%w: %q is not a valid wildcard hostname", ErrInvalid, host)
		}
		if tlsSecret == "" {
			return nil, fmt.Errorf("%w: wildcard host %q needs tlsSecret — the name of a pre-provisioned wildcard cert Secret (Let's Encrypt can't HTTP-01 a wildcard; mint one via a cert-manager DNS-01 Certificate first)", ErrInvalid, host)
		}
		if !validSecretName(tlsSecret) {
			return nil, fmt.Errorf("%w: %q is not a valid Secret name", ErrInvalid, tlsSecret)
		}
	} else {
		if !validHostname(host) {
			return nil, fmt.Errorf("%w: %q is not a valid hostname", ErrInvalid, host)
		}
		if tlsSecret != "" {
			return nil, fmt.Errorf("%w: tlsSecret is only supported for wildcard hosts (*.example.com); non-wildcard hosts get a cert-manager cert automatically", ErrInvalid)
		}
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	envCRName := envCRNameFor(project, service, envName)

	// Pre-flight: same host can't be on another env in the same
	// project (or any namespace really, but k8s Ingress admission
	// catches cross-NS). Catching it here gives a nicer error than
	// the operator's "Ingress conflict: another Ingress claims X".
	existing, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for conflict check: %w", err)
	}
	for i := range existing {
		if existing[i].Name == envCRName {
			continue
		}
		if strings.EqualFold(existing[i].Spec.Host, host) {
			return nil, fmt.Errorf("%w: %q is the primary host of env %q", ErrConflict, host, existing[i].Name)
		}
		for _, h := range existing[i].Spec.AdditionalHosts {
			if strings.EqualFold(h, host) {
				return nil, fmt.Errorf("%w: %q already on env %q", ErrConflict, host, existing[i].Name)
			}
		}
		for _, w := range existing[i].Spec.WildcardDomains {
			if strings.EqualFold(w.Host, host) {
				return nil, fmt.Errorf("%w: %q already on env %q", ErrConflict, host, existing[i].Name)
			}
		}
	}

	updated, err := s.updateOwnedEnvWithRetry(ctx, ns, project, envCRName, func(env *kube.KusoEnvironment) error {
		if wildcard {
			// Idempotent upsert: same host re-add updates the secret
			// (lets the operator rotate to a renamed cert secret).
			for i := range env.Spec.WildcardDomains {
				if strings.EqualFold(env.Spec.WildcardDomains[i].Host, host) {
					env.Spec.WildcardDomains[i].TLSSecret = tlsSecret
					return nil
				}
			}
			env.Spec.WildcardDomains = append(env.Spec.WildcardDomains, kube.KusoWildcardDomain{
				Host: host, TLSSecret: tlsSecret,
			})
			return nil
		}
		// Idempotent: re-adding an existing host is a no-op. The CLI
		// retry path benefits; the UI's "+ Add" doesn't repeat-fire
		// but the safety still matters.
		for _, h := range env.Spec.AdditionalHosts {
			if strings.EqualFold(h, host) {
				return nil
			}
		}
		env.Spec.AdditionalHosts = append(env.Spec.AdditionalHosts, host)
		env.Spec.TLSHosts = computeTLSHosts(env.Spec.Host, env.Spec.AdditionalHosts)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update env: %w", err)
	}
	return updated, nil
}

// RemoveEnvDomain drops a host from env.Spec.AdditionalHosts.
// Idempotent: removing an absent host returns the env unchanged with
// no error (matches the UI's "I clicked X twice" gesture).
func (s *Service) RemoveEnvDomain(ctx context.Context, project, service, envName, host string) (*kube.KusoEnvironment, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, fmt.Errorf("%w: host required", ErrInvalid)
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	envCRName := envCRNameFor(project, service, envName)
	updated, err := s.updateOwnedEnvWithRetry(ctx, ns, project, envCRName, func(env *kube.KusoEnvironment) error {
		out := env.Spec.AdditionalHosts[:0]
		for _, h := range env.Spec.AdditionalHosts {
			if !strings.EqualFold(h, host) {
				out = append(out, h)
			}
		}
		env.Spec.AdditionalHosts = out
		env.Spec.TLSHosts = computeTLSHosts(env.Spec.Host, env.Spec.AdditionalHosts)
		// Wildcard entries are removed by the same verb — one namespace
		// of hosts for the user, two storage fields internally.
		wout := env.Spec.WildcardDomains[:0]
		for _, w := range env.Spec.WildcardDomains {
			if !strings.EqualFold(w.Host, host) {
				wout = append(wout, w)
			}
		}
		if len(wout) == 0 {
			env.Spec.WildcardDomains = nil
		} else {
			env.Spec.WildcardDomains = wout
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update env: %w", err)
	}
	return updated, nil
}

// validSecretName reports whether s is a valid k8s Secret name
// (RFC 1123 subdomain, ≤253 chars).
func validSecretName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	return secretNameRE.MatchString(s)
}

var secretNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// SetEnvDomains replaces the entire AdditionalHosts list. Used by the
// dashboard's textarea-style editor; for incremental add/remove use
// AddEnvDomain / RemoveEnvDomain.
//
// Same cross-env conflict check as AddEnvDomain, applied to every
// host in the new list.
func (s *Service) SetEnvDomains(ctx context.Context, project, service, envName string, hosts []string) (*kube.KusoEnvironment, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	envCRName := envCRNameFor(project, service, envName)

	// Normalize: lowercase + trim + dedupe.
	seen := map[string]bool{}
	clean := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		if !validHostname(h) {
			return nil, fmt.Errorf("%w: %q is not a valid hostname", ErrInvalid, h)
		}
		seen[h] = true
		clean = append(clean, h)
	}

	// Cross-env conflict scan — every new host must NOT be claimed by
	// any other env in this project (either as primary host or
	// additional host).
	existing, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
	})
	if err != nil {
		return nil, fmt.Errorf("list envs for conflict check: %w", err)
	}
	for _, h := range clean {
		for i := range existing {
			if existing[i].Name == envCRName {
				continue
			}
			if strings.EqualFold(existing[i].Spec.Host, h) {
				return nil, fmt.Errorf("%w: %q is the primary host of env %q", ErrConflict, h, existing[i].Name)
			}
			for _, eh := range existing[i].Spec.AdditionalHosts {
				if strings.EqualFold(eh, h) {
					return nil, fmt.Errorf("%w: %q already on env %q", ErrConflict, h, existing[i].Name)
				}
			}
		}
	}

	updated, err := s.updateOwnedEnvWithRetry(ctx, ns, project, envCRName, func(env *kube.KusoEnvironment) error {
		env.Spec.AdditionalHosts = clean
		env.Spec.TLSHosts = computeTLSHosts(env.Spec.Host, env.Spec.AdditionalHosts)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update env: %w", err)
	}
	return updated, nil
}

// SetEnvScopedVar upserts a single env var directly on ONE environment's
// CR (env.Spec.EnvVars). Unlike the service-level SetEnvVar — which writes
// the service spec and propagates to every env — this writes the env CR
// leaf the chart reads, so the value applies ONLY to that env. It survives
// later service-level propagation as a per-env override: the merge in
// resolveSharedEnvKeysForEnv layers env.Spec.EnvVars on top of the service
// vars (env wins for a duplicate key), so e.g. a staging env can hold
// NEXT_PUBLIC_ENVIRONMENT=staging while production stays =production.
//
// No propagation call: the env CR is the leaf (mirrors AddEnvDomain).
func (s *Service) SetEnvScopedVar(ctx context.Context, project, service, envName, name string, req SetEnvVarRequest) (*kube.KusoEnvironment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: env var name required", ErrInvalid)
	}
	if !validEnvVarName(name) {
		return nil, fmt.Errorf("%w: env var name %q must match [A-Za-z_][A-Za-z0-9_]*", ErrInvalid, name)
	}
	hasValue := req.Value != ""
	hasRef := req.SecretRef != nil && req.SecretRef.Name != "" && req.SecretRef.Key != ""
	if hasValue == hasRef {
		return nil, fmt.Errorf("%w: exactly one of value or secretRef must be set", ErrInvalid)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	envCRName := envCRNameFor(project, service, envName)

	next := kube.KusoEnvVar{Name: name}
	if hasValue {
		// Resolve ${{ addon.KEY }} / ${{ svc.URL }} refs at set time,
		// exactly like the service-level SetEnv path does. Without this
		// the raw `${{ }}` literal was stored verbatim — so (a) the pod
		// got the literal string instead of the resolved value, and
		// (b) the next service-level propagation dropped it as an
		// "unresolved ref" (extractEnvOnlyOverrides), silently destroying
		// the user's per-env override. Rewriting here makes a genuine
		// override always a concrete value or a resolved secretKeyRef —
		// the invariant that drop logic relies on.
		svcResolver, rerr := s.buildServiceResolver(ctx, project, ns)
		if rerr != nil {
			return nil, fmt.Errorf("resolve services: %w", rerr)
		}
		addonResolver := s.buildAddonResolver(ctx, project)
		rewritten, rerr := RewriteEnvVar(EnvVar{Name: name, Value: req.Value}, svcResolver, addonResolver)
		if rerr != nil {
			return nil, rerr
		}
		next.Value = rewritten.Value
		next.ValueFrom = rewritten.ValueFrom
	} else {
		if err := s.validateSecretRefName(ctx, project, service, req.SecretRef.Name); err != nil {
			return nil, err
		}
		next.ValueFrom = map[string]any{
			"secretKeyRef": map[string]any{"name": req.SecretRef.Name, "key": req.SecretRef.Key},
		}
	}

	return s.updateOwnedEnvWithRetry(ctx, ns, project, envCRName, func(env *kube.KusoEnvironment) error {
		// Record this name as a DELIBERATE per-env override so later
		// service-level propagation preserves it instead of re-stamping
		// the service value over it. This explicit marker is what lets
		// extractEnvOnlyOverrides tell a genuine override from a stale
		// inherited seed (see KusoEnvironmentSpec.EnvOverrides).
		if !slices.Contains(env.Spec.EnvOverrides, name) {
			env.Spec.EnvOverrides = append(env.Spec.EnvOverrides, name)
		}
		for i := range env.Spec.EnvVars {
			if env.Spec.EnvVars[i].Name == name {
				env.Spec.EnvVars[i] = next
				return nil
			}
		}
		env.Spec.EnvVars = append(env.Spec.EnvVars, next)
		return nil
	})
}

// UnsetEnvScopedVar removes a per-env override from one env CR. ErrNotFound
// when the var isn't present on that env (distinguishes idempotent retry
// from a typo).
func (s *Service) UnsetEnvScopedVar(ctx context.Context, project, service, envName, name string) (*kube.KusoEnvironment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: env var name required", ErrInvalid)
	}
	mu := s.lockService(project, service)
	defer mu.Unlock()
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	envCRName := envCRNameFor(project, service, envName)
	var notFound bool
	updated, err := s.updateOwnedEnvWithRetry(ctx, ns, project, envCRName, func(env *kube.KusoEnvironment) error {
		notFound = false
		out := make([]kube.KusoEnvVar, 0, len(env.Spec.EnvVars))
		found := false
		for _, e := range env.Spec.EnvVars {
			if e.Name == name {
				found = true
				continue
			}
			out = append(out, e)
		}
		if !found {
			notFound = true
			return kube.ErrAbortRetry
		}
		env.Spec.EnvVars = out
		// Drop the override marker too — once the user unsets their
		// per-env value, the var should fall back to inheriting from
		// the service on the next propagation, not stay pinned to a
		// now-absent override.
		env.Spec.EnvOverrides = slices.DeleteFunc(env.Spec.EnvOverrides, func(s string) bool { return s == name })
		return nil
	})
	if notFound {
		return nil, fmt.Errorf("%w: env var %q", ErrNotFound, name)
	}
	return updated, err
}

// envCRNameFor returns the kube CR name for a (project, service, env)
// tuple. Production = "<project>-<service>-production"; custom envs
// follow the same shape. Mirror of the construction in AddService.
func envCRNameFor(project, service, envName string) string {
	// Service may already carry the "<project>-" prefix; if so, don't
	// double it.
	short := strings.TrimPrefix(service, project+"-")
	return fmt.Sprintf("%s-%s-%s", project, short, envName)
}
