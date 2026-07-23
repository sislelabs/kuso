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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// AddDomainRequest is the wire shape for POST .../domains.
type AddDomainRequest struct {
	Host string `json:"host"`
	TLS  bool   `json:"tls"`
	// TLSSecret names a pre-provisioned TLS secret; required for (and
	// only valid with) wildcard hosts. See kube.KusoWildcardDomain.
	TLSSecret string `json:"tlsSecret,omitempty"`
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
	wildcard := strings.HasPrefix(host, "*.")
	if wildcard {
		// Wildcard hosts are validated in depth (incl. the tlsSecret
		// requirement) by AddEnvDomain below; here just gate the shape
		// so an invalid host never lands on the service spec.
		if !validHostname(strings.TrimPrefix(host, "*.")) {
			return nil, fmt.Errorf("%w: %q is not a valid wildcard hostname", ErrInvalid, host)
		}
		if strings.TrimSpace(req.TLSSecret) == "" {
			return nil, fmt.Errorf("%w: wildcard host %q needs tlsSecret — the name of a pre-provisioned wildcard cert Secret", ErrInvalid, host)
		}
	} else {
		if !validHostname(host) {
			return nil, fmt.Errorf("%w: %q is not a valid hostname", ErrInvalid, host)
		}
		if req.TLS && !isPublicFQDN(host) {
			return nil, fmt.Errorf("%w: %q can't get a Let's Encrypt cert (not a public FQDN — single-label or reserved TLD); add it with TLS off if you only need HTTP routing", ErrInvalid, host)
		}
	}

	// In-process mutex guards same-replica races. Multi-replica
	// races land on the kube optimistic-concurrency check below:
	// updateWithRetry re-runs the duplicate scan against the live
	// resourceVersion on conflict, so the second write doesn't
	// silently overwrite the first.
	mu := s.lockService(project, service)
	defer mu.Unlock()

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	var dupConflict bool
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		dupConflict = false
		for i := range svc.Spec.Domains {
			if strings.EqualFold(svc.Spec.Domains[i].Host, host) {
				if svc.Spec.Domains[i].TLS == req.TLS && svc.Spec.Domains[i].TLSSecret == req.TLSSecret {
					dupConflict = true
					return kube.ErrAbortRetry
				}
				svc.Spec.Domains[i].TLS = req.TLS
				svc.Spec.Domains[i].TLSSecret = req.TLSSecret
				return nil
			}
		}
		svc.Spec.Domains = append(svc.Spec.Domains, kube.KusoDomain{Host: host, TLS: req.TLS, TLSSecret: req.TLSSecret})
		return nil
	})
	if dupConflict {
		return nil, fmt.Errorf("%w: domain %q already configured", ErrConflict, host)
	}
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	// Mirror the domain onto the PRODUCTION env so it actually reaches
	// the Ingress + TLS cert. The chart renders hosts/certs from the env
	// CR's host + additionalHosts + tlsHosts — NOT from service.spec.
	// domains — so a service-level write alone is invisible to routing
	// (the changed.Domains propagation branch is a deliberate no-op since
	// v0.16.19). We target production ONLY: staging/custom envs serve
	// different hostnames and must not auto-inherit (that mirror-to-every-
	// env behavior is exactly what v0.16.19 removed to stop staging from
	// claiming production's domain). AddEnvDomain is idempotent + reuses
	// the cross-env conflict check + computeTLSHosts. For TLS=false hosts
	// it still routes (additionalHosts) but won't be added to tlsHosts.
	if _, perr := s.AddEnvDomain(ctx, project, service, "production", host, req.TLSSecret); perr != nil {
		// A missing production env (rare: service mid-create) shouldn't
		// fail the service-level write — the domain is recorded on the
		// service and a later env reconcile/redeploy can re-mirror it.
		if !errors.Is(perr, ErrNotFound) && !apierrors.IsNotFound(perr) {
			return updated, fmt.Errorf("mirror domain to production env: %w", perr)
		}
	}
	return updated, nil
}

// RemoveDomain drops a domain from spec.domains by host. ErrNotFound
// when the host isn't present — distinguishes "I deleted it from the
// UI but it was already gone" (idempotent retry) from "I tried to
// delete it and it really wasn't there" (UI/CLI bug).
//
// Uses UpdateKusoServiceWithRetry so a helm-operator status patch
// landing between our Get and Update doesn't 409 the user's edit.
// AddDomain showed this pattern; the delete path used to use a
// plain UpdateKusoService and silently lost writes under operator
// churn.
func (s *Service) RemoveDomain(ctx context.Context, project, service, host string) (*kube.KusoService, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, fmt.Errorf("%w: host required", ErrInvalid)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	var notFound bool
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		notFound = false
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
			notFound = true
			return kube.ErrAbortRetry
		}
		svc.Spec.Domains = out
		return nil
	})
	if notFound {
		return nil, fmt.Errorf("%w: domain %q", ErrNotFound, host)
	}
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	// Mirror the removal onto the production env (see AddDomain). Idempotent
	// — RemoveEnvDomain on an absent host is a no-op, so this is safe even
	// if the host was never mirrored.
	if _, perr := s.RemoveEnvDomain(ctx, project, service, "production", host); perr != nil {
		if !errors.Is(perr, ErrNotFound) && !apierrors.IsNotFound(perr) {
			return updated, fmt.Errorf("mirror domain removal to production env: %w", perr)
		}
	}
	return updated, nil
}

// SetEnvVarRequest is the wire shape for PUT .../env-vars/:name.
//
// Exactly one of three modes must be set:
//   - Value: a literal env var written to spec.envVars.
//   - SecretRef: a valueFrom secretKeyRef pointer written to spec.envVars.
//   - SecretValue: a secret VALUE stored in the kuso-managed
//     <project>-<service>-secrets Secret (the one the pod already
//     envFrom-mounts). spec.envVars is NOT touched — the value lives
//     only in the Secret, and the editor surfaces the key via the
//     managed-secret enrichment path (see managed_secret_env.go). This
//     is how a key like WETRAVEL_API_KEY becomes editable without ever
//     landing its plaintext on the KusoService CR.
type SetEnvVarRequest struct {
	Value       string                  `json:"value,omitempty"`
	SecretRef   *SetEnvVarSecretRefBody `json:"secretRef,omitempty"`
	SecretValue *string                 `json:"secretValue,omitempty"`
}

type SetEnvVarSecretRefBody struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// SetEnvVar adds or updates a single env var on the service. Existing
// var with the same name is overwritten; new var is appended. Always
// idempotent — re-running with the same value is a no-op write but
// returns 200, not 409.
//
// Uses UpdateKusoServiceWithRetry so a helm-operator status patch
// between Get and Update doesn't lose the user's edit.
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
	// A non-nil SecretValue pointer is the signal, even for the empty
	// string — an empty secret value is a legitimate write (clearing a
	// key's value while keeping the key present), distinct from "field
	// absent". Value/SecretRef use presence-by-content because their
	// wire shapes carry no way to distinguish empty-set from unset.
	hasSecretValue := req.SecretValue != nil
	if count := btoi(hasValue) + btoi(hasRef) + btoi(hasSecretValue); count != 1 {
		return nil, fmt.Errorf("%w: exactly one of value, secretRef, or secretValue must be set", ErrInvalid)
	}

	mu := s.lockService(project, service)
	defer mu.Unlock()

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}

	// SecretValue mode: write the value into the kuso-managed
	// <project>-<service>-secrets Secret and roll the pods, WITHOUT
	// touching spec.envVars. The key becomes visible in the editor via
	// EnrichServiceWithManagedSecretKeys. We return the (unchanged)
	// service spec so the caller can re-baseline like the other branches.
	if hasSecretValue {
		if err := s.upsertManagedSecretKey(ctx, ns, project, service, name, *req.SecretValue); err != nil {
			return nil, err
		}
		// Value-only Secret changes do NOT restart pods on their own —
		// the helm chart re-renders the Deployment only when a watched
		// env-CR field changes. Bump spec.secretsRev on every owned env
		// (mirrors secrets.bumpRev) so the new value actually reaches a
		// running pod. Best-effort: the Secret is the source of truth and
		// the next reconcile/redeploy re-picks it up if the bump misses.
		if err := s.bumpSecretsRevForService(ctx, ns, project, service); err != nil {
			return nil, fmt.Errorf("bump secretsRev after secret write: %w", err)
		}
		svc, gerr := s.GetService(ctx, project, service)
		if gerr != nil {
			return nil, gerr
		}
		return svc, nil
	}

	next := kube.KusoEnvVar{Name: name}
	if hasValue {
		next.Value = req.Value
	} else {
		if err := s.validateSecretRefName(ctx, project, service, req.SecretRef.Name); err != nil {
			return nil, err
		}
		next.ValueFrom = map[string]any{
			"secretKeyRef": map[string]any{
				"name": req.SecretRef.Name,
				"key":  req.SecretRef.Key,
			},
		}
	}

	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
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
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	if perr := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{EnvVars: true}); perr != nil {
		return nil, fmt.Errorf("propagate envVars to envs: %w", perr)
	}
	return updated, nil
}

// UnsetEnvVar removes a single env var by name. ErrNotFound when the
// var isn't present.
//
// Uses UpdateKusoServiceWithRetry so a helm-operator status patch
// between Get and Update doesn't lose the user's unset.
func (s *Service) UnsetEnvVar(ctx context.Context, project, service, name string) (*kube.KusoService, error) {
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

	var notFound bool
	updated, err := s.updateOwnedServiceWithRetry(ctx, ns, project, service, func(svc *kube.KusoService) error {
		notFound = false
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
			notFound = true
			return kube.ErrAbortRetry
		}
		svc.Spec.EnvVars = out
		return nil
	})
	if notFound {
		// Not a spec.envVars entry — it may be a managed-secret-only key
		// (set via SecretValue mode, which never touches spec.envVars).
		// Remove it from <project>-<service>-secrets and roll the pods.
		removed, rerr := s.removeManagedSecretKey(ctx, ns, project, service, name)
		if rerr != nil {
			return nil, rerr
		}
		if !removed {
			return nil, fmt.Errorf("%w: env var %q", ErrNotFound, name)
		}
		if berr := s.bumpSecretsRevForService(ctx, ns, project, service); berr != nil {
			return nil, fmt.Errorf("bump secretsRev after secret unset: %w", berr)
		}
		svc, gerr := s.GetService(ctx, project, service)
		if gerr != nil {
			return nil, gerr
		}
		return svc, nil
	}
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	if perr := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{EnvVars: true}); perr != nil {
		return nil, fmt.Errorf("propagate envVars to envs: %w", perr)
	}
	return updated, nil
}

// btoi returns 1 for true, 0 for false — used to count how many of a
// set of mutually-exclusive request modes were supplied.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// upsertManagedSecretKey writes (key, value) into the kuso-managed
// <project>-<service>-secrets Secret via read-modify-write, preserving
// every OTHER key AND all annotations (notably the
// secrets.kuso.sislelabs.com/generated-* markers). Creates the Secret
// with the kuso managed labels if it doesn't exist yet.
//
// The chart marks this Secret optional and the kusoenvironment
// envFromSecrets already references <svc>-secrets on every non-preview
// env, so a freshly-created Secret is picked up without any env-CR wiring
// change here (see managed_secret_env.go / secrets.attachToAllEnvs).
func (s *Service) upsertManagedSecretKey(ctx context.Context, ns, project, service, key, value string) error {
	name := kube.ServiceSecretName(project, service)
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				// Mirror the labels kuso stamps on the resources it owns
				// (app.kubernetes.io/managed-by=kuso-server) plus the
				// project/service selectors used across the codebase, so
				// the Secret is discoverable + attributable like the rest.
				Labels: map[string]string{
					kube.ManagedByLabel: "kuso-server",
					kube.LabelProject:   project,
					kube.LabelService:   service,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: []byte(value)},
		}
		if _, cerr := secrets.Create(ctx, sec, metav1.CreateOptions{}); cerr != nil {
			// A concurrent creator won the race — fall through to a
			// read-modify-write patch so we don't clobber its keys.
			if apierrors.IsAlreadyExists(cerr) {
				return s.patchManagedSecretKey(ctx, ns, name, key, value)
			}
			return fmt.Errorf("create secret %s: %w", name, cerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read secret %s: %w", name, err)
	}
	// RMW: copy the existing Data, set only our key, write the whole
	// object back. This preserves every other key; Update carries the
	// existing annotations/labels through untouched (we never clear them).
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	sec.Data[key] = []byte(value)
	if _, uerr := secrets.Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
		return fmt.Errorf("update secret %s: %w", name, uerr)
	}
	return nil
}

// patchManagedSecretKey is the create-race fallback: merge-patch a single
// key so a Secret another writer just created keeps all of its keys +
// annotations. Mirrors secrets.upsertKey's merge-patch shape.
func (s *Service) patchManagedSecretKey(ctx context.Context, ns, name, key, value string) error {
	// Merge-patch a single key via .stringData — the apiserver writes it
	// into .data (base64-encoding for us) and merges it in without
	// disturbing the other keys or the annotations. Using stringData
	// avoids doing the base64 dance by hand.
	body, err := json.Marshal(map[string]any{
		"stringData": map[string]string{key: value},
	})
	if err != nil {
		return fmt.Errorf("marshal secret patch: %w", err)
	}
	if _, perr := s.Kube.Clientset.CoreV1().Secrets(ns).
		Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{}); perr != nil {
		return fmt.Errorf("patch secret %s: %w", name, perr)
	}
	return nil
}

// removeManagedSecretKey deletes key from <project>-<service>-secrets via
// read-modify-write, leaving every OTHER key and all annotations intact.
// Returns (false, nil) when the Secret or key is absent so the caller can
// map that to ErrNotFound. Deletes the Secret entirely when the removed
// key was its last one (mirrors secrets.UnsetKey), so an empty managed
// Secret doesn't linger.
func (s *Service) removeManagedSecretKey(ctx context.Context, ns, project, service, key string) (bool, error) {
	name := kube.ServiceSecretName(project, service)
	secrets := s.Kube.Clientset.CoreV1().Secrets(ns)
	sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read secret %s: %w", name, err)
	}
	if _, ok := sec.Data[key]; !ok {
		return false, nil
	}
	if len(sec.Data) == 1 {
		if derr := secrets.Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			return false, fmt.Errorf("delete secret %s: %w", name, derr)
		}
		return true, nil
	}
	delete(sec.Data, key)
	if _, uerr := secrets.Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
		return false, fmt.Errorf("update secret %s: %w", name, uerr)
	}
	return true, nil
}

// bumpSecretsRevForService stamps a fresh spec.secretsRev on every owned
// env so the helm-operator re-renders the Deployment and the pods pick up
// the changed Secret value. This is the minimal rollout equivalent to what
// propagateChangedToEnvs achieves for spec-field changes — a value-only
// Secret write leaves the env CRs otherwise untouched, so without this the
// running pods keep the stale value until the next unrelated save. Mirrors
// secrets.bumpRev (same {"spec":{"secretsRev":...}} patch shape).
func (s *Service) bumpSecretsRevForService(ctx context.Context, ns, project, service string) error {
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		return fmt.Errorf("list envs for secretsRev bump: %w", err)
	}
	rev := strconv.FormatInt(time.Now().UnixMilli(), 10)
	patch := fmt.Sprintf(`{"spec":{"secretsRev":%q}}`, rev)
	var errs []error
	for i := range envs {
		if _, perr := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, envs[i].Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			errs = append(errs, fmt.Errorf("patch env %s: %w", envs[i].Name, perr))
		}
	}
	return errors.Join(errs...)
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
