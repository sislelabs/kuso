// Delta operations on KusoService.spec.domains and spec.envVars.
//
// Why these exist:
//
// PatchService takes a full domains slice / envVars slice and overwrites
// the field. That's correct semantics when the client genuinely wants to
// replace the whole list — but it's the wrong semantics for "user added
// one domain in their browser tab" because two clients editing the same
// service simultaneously will both PUT a complete list, and last-write-
// wins. Whichever client saved second silently throws away the other's
// edit.
//
// The fix is to express what the user actually meant: ADD this one
// domain, REMOVE that one, SET this env var, UNSET that one. The server
// fetches the current spec under a per-service mutex, applies the
// delta, and writes it back. Concurrent AddDomain("a") + AddDomain("b")
// both land safely; concurrent SetEnvVar("FOO", x) + SetEnvVar("FOO", y)
// serialise into a deterministic last-write that the user can see.
//
// These methods sit alongside PatchService, not as a replacement —
// some flows really do want "replace the whole list" (initial provision,
// `kuso apply` from a YAML file). The UI's per-row add / remove buttons
// should call these; bulk forms still go through PatchService.

package projects

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
)

// AddDomainRequest is the wire shape for POST .../domains.
type AddDomainRequest struct {
	Host string `json:"host"`
	TLS  bool   `json:"tls"`
}

// AddDomain appends a domain to spec.domains, deduplicating by host.
// The caller's `tls` value wins on a duplicate (so re-adding a host
// with a different TLS setting is a way to flip the flag).
//
// Returns ErrConflict if the host is already present with the same TLS
// flag — that way an idempotent retry surfaces clearly without a noop
// round-trip rewriting the same spec. (HTTP layer maps to 409.)
func (s *Service) AddDomain(ctx context.Context, project, service string, req AddDomainRequest) (*kube.KusoService, error) {
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if host == "" {
		return nil, fmt.Errorf("%w: host required", ErrInvalid)
	}
	if !validHostname(host) {
		return nil, fmt.Errorf("%w: %q is not a valid hostname", ErrInvalid, host)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	svc, ns, err := s.fetchServiceForDelta(ctx, project, service)
	if err != nil {
		return nil, err
	}

	for i := range svc.Spec.Domains {
		if strings.EqualFold(svc.Spec.Domains[i].Host, host) {
			if svc.Spec.Domains[i].TLS == req.TLS {
				return svc, fmt.Errorf("%w: domain %q already configured", ErrConflict, host)
			}
			// Existing host with different TLS — flip the flag.
			svc.Spec.Domains[i].TLS = req.TLS
			return s.persistDomains(ctx, ns, project, service, svc)
		}
	}

	svc.Spec.Domains = append(svc.Spec.Domains, kube.KusoDomain{Host: host, TLS: req.TLS})
	return s.persistDomains(ctx, ns, project, service, svc)
}

// RemoveDomain drops a domain from spec.domains by host. ErrNotFound
// when the host isn't present — distinguishes "I deleted it from the
// UI but it was already gone" (idempotent retry) from "I tried to
// delete it and it really wasn't there" (UI/CLI bug).
func (s *Service) RemoveDomain(ctx context.Context, project, service, host string) (*kube.KusoService, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, fmt.Errorf("%w: host required", ErrInvalid)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	svc, ns, err := s.fetchServiceForDelta(ctx, project, service)
	if err != nil {
		return nil, err
	}

	out := make([]kube.KusoDomain, 0, len(svc.Spec.Domains))
	found := false
	for _, d := range svc.Spec.Domains {
		if strings.EqualFold(d.Host, host) {
			found = true
			continue
		}
		out = append(out, d)
	}
	if !found {
		return nil, fmt.Errorf("%w: domain %q", ErrNotFound, host)
	}
	svc.Spec.Domains = out
	return s.persistDomains(ctx, ns, project, service, svc)
}

// SetEnvVarRequest is the wire shape for PUT .../env-vars/:name.
//
// Setting Value sets a literal env var. SecretRef sets a valueFrom
// secretKeyRef pointer. Exactly one must be present.
type SetEnvVarRequest struct {
	Value     string                  `json:"value,omitempty"`
	SecretRef *SetEnvVarSecretRefBody `json:"secretRef,omitempty"`
}

type SetEnvVarSecretRefBody struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SetEnvVar adds or updates a single env var on the service. Existing
// var with the same name is overwritten; new var is appended. Always
// idempotent — re-running with the same value is a no-op write but
// returns 200, not 409.
func (s *Service) SetEnvVar(ctx context.Context, project, service, name string, req SetEnvVarRequest) (*kube.KusoService, error) {
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

	svc, ns, err := s.fetchServiceForDelta(ctx, project, service)
	if err != nil {
		return nil, err
	}

	next := kube.KusoEnvVar{Name: name}
	if hasValue {
		next.Value = req.Value
	} else {
		next.ValueFrom = map[string]any{
			"secretKeyRef": map[string]any{
				"name": req.SecretRef.Name,
				"key":  req.SecretRef.Key,
			},
		}
	}

	replaced := false
	for i := range svc.Spec.EnvVars {
		if svc.Spec.EnvVars[i].Name == name {
			svc.Spec.EnvVars[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		svc.Spec.EnvVars = append(svc.Spec.EnvVars, next)
	}

	return s.persistEnvVars(ctx, ns, project, service, svc)
}

// UnsetEnvVar removes a single env var by name. ErrNotFound when the
// var isn't present.
func (s *Service) UnsetEnvVar(ctx context.Context, project, service, name string) (*kube.KusoService, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: env var name required", ErrInvalid)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	svc, ns, err := s.fetchServiceForDelta(ctx, project, service)
	if err != nil {
		return nil, err
	}

	out := make([]kube.KusoEnvVar, 0, len(svc.Spec.EnvVars))
	found := false
	for _, e := range svc.Spec.EnvVars {
		if e.Name == name {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return nil, fmt.Errorf("%w: env var %q", ErrNotFound, name)
	}
	svc.Spec.EnvVars = out
	return s.persistEnvVars(ctx, ns, project, service, svc)
}

// fetchServiceForDelta is the common preamble for every delta op.
// Returns the namespace alongside the service so the persist step
// doesn't have to re-resolve it.
func (s *Service) fetchServiceForDelta(ctx context.Context, project, service string) (*kube.KusoService, string, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, "", err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, "", err
	}
	return svc, ns, nil
}

// persistDomains writes the updated KusoService and propagates the
// domain change to every env. Returns the updated CR even when
// propagation fails — the spec is durable and the next save retries.
func (s *Service) persistDomains(ctx context.Context, ns, project, service string, svc *kube.KusoService) (*kube.KusoService, error) {
	defer s.invalidateDescribe(project)
	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	// Best-effort env propagation — kusoenvironment chart reads only
	// the env CR, so without this the new domain doesn't reach the
	// Ingress until the next env-touching write.
	if err := s.propagateDomainsToEnvs(ctx, ns, project, service, updated); err != nil {
		return updated, nil //nolint:nilerr // intentional: spec is durable, env retry on next write
	}
	return updated, nil
}

// persistEnvVars writes the updated KusoService. Env vars on the
// service spec do not propagate to env CRs through the helm chart
// directly — services pull from envFromSecrets and per-env spec.envVars
// is the env-level override path. So we only invalidate caches and
// return.
func (s *Service) persistEnvVars(ctx context.Context, ns, project, service string, svc *kube.KusoService) (*kube.KusoService, error) {
	defer s.invalidateDescribe(project)
	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	return updated, nil
}

// validHostname is a permissive RFC-1123-ish check. We don't try to
// match every edge case the apiserver / cert-manager would accept; we
// just refuse the obviously-bad input that would silently break the
// Ingress reconcile. cert-manager + traefik will reject anything more
// subtle with a clearer error than we could.
func validHostname(h string) bool {
	if len(h) < 1 || len(h) > 253 {
		return false
	}
	if strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, r := range label {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			isDash := r == '-'
			if !isAlnum && !isDash {
				return false
			}
			if (i == 0 || i == len(label)-1) && isDash {
				return false
			}
		}
	}
	return true
}

// validEnvVarName mirrors POSIX env-var naming: leading letter or
// underscore, then alnum + underscore. We're stricter than kube
// (which is mostly permissive) so the operator chart doesn't have
// to guard against shell-incompatible names downstream.
func validEnvVarName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for i, r := range name {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isUnder := r == '_'
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !isLetter && !isUnder {
				return false
			}
			continue
		}
		if !isLetter && !isUnder && !isDigit {
			return false
		}
	}
	return true
}
