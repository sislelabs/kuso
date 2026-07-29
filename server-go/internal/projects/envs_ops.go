package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// ListEnvironments returns the environments in a project (label-filtered).
func (s *Service) ListEnvironments(ctx context.Context, project string) ([]kube.KusoEnvironment, error) {
	return s.listEnvsForProject(ctx, project)
}

// GetEnvironment loads one environment by name.
func (s *Service) GetEnvironment(ctx context.Context, project, env string) (*kube.KusoEnvironment, error) {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	e, err := s.Kube.GetKusoEnvironment(ctx, ns, env)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Spec.Project != project {
		// Don't leak cross-project envs even if the URL is guessed.
		return nil, ErrNotFound
	}
	return e, nil
}

// SweepExpiredPreviews scans every preview KusoEnvironment in the
// configured namespace and deletes any whose spec.ttl.expiresAt is in
// the past. Webhooks are the primary teardown mechanism; this is the
// safety net for missed close events / suspended Apps / past outages.
//
// Returns the number of envs deleted. Errors against individual envs
// are logged via the supplied callback (or swallowed when nil) so one
// flaky teardown doesn't stop the sweep.
func (s *Service) SweepExpiredPreviews(ctx context.Context, onErr func(name string, err error)) (int, error) {
	// Build the set of namespaces to scan: home + every distinct
	// spec.namespace declared by a KusoProject. Dedupe so we don't
	// double-sweep the home ns when a project is unset.
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{s.Namespace: true}
	nss := []string{s.Namespace}
	for _, p := range projects {
		ns := p.Spec.Namespace
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		nss = append(nss, ns)
	}

	now := time.Now().UTC()
	deleted := 0
	for _, ns := range nss {
		envs, err := s.Kube.ListKusoEnvironments(ctx, ns)
		if err != nil {
			if onErr != nil {
				onErr("ns:"+ns, err)
			}
			continue
		}
		for _, e := range envs {
			if e.Spec.Kind != "preview" || e.Spec.TTL == nil || e.Spec.TTL.ExpiresAt == "" {
				continue
			}
			exp, err := time.Parse(time.RFC3339, e.Spec.TTL.ExpiresAt)
			if err != nil || !exp.Before(now) {
				continue
			}
			// Route through the full DeleteEnvironment rather than
			// DeleteKusoEnvironment. The bare CR delete leaves the per-env
			// Secret, the PR's addon clones (StatefulSet + data PVC) and
			// the cert-manager TLS Secrets behind — nothing GCs those, so
			// every preview that lapses by TTL instead of a PR-close
			// webhook orphaned its storage forever. TTL expiry is the
			// HIGHER-volume path (a PR opened and abandoned never fires a
			// close event), so this was the main orphan source.
			// DeleteEnvironment is documented idempotent and resumable, so
			// it is safe to call here and safe to retry on the next tick.
			proj := e.Spec.Project
			if proj == "" {
				proj = e.Labels[kube.LabelProject]
			}
			if proj == "" {
				// Can't resolve the project — fall back to the bare CR
				// delete so an unlabelled env still expires rather than
				// lingering past its TTL forever.
				if err := s.Kube.DeleteKusoEnvironment(ctx, ns, e.Name); err != nil {
					if onErr != nil {
						onErr(e.Name, err)
					}
					continue
				}
				deleted++
				continue
			}
			if err := s.DeleteEnvironment(ctx, proj, e.Name); err != nil && !apierrors.IsNotFound(err) {
				if onErr != nil {
					onErr(e.Name, err)
				}
				continue
			}
			// No describe cache to invalidate: Describe's list calls are
			// informer-served (see the removed describeCache note on the
			// Service struct), so the deleted env drops out of the next
			// Describe as soon as the informer observes the delete on its
			// watch stream. The previous empty if-block here was a dead
			// no-op left from the cached-Describe era.
			deleted++
		}
	}
	return deleted, nil
}

// DeleteEnvironment removes a preview env. Production envs cannot be
// deleted directly — service deletion handles those. Mirrors the TS
// behaviour because preview teardown is the legitimate use case here.
//
// We also wipe the per-env Secret (the helm-operator's finalizer tears
// down the helm release but leaves the underlying Secret CR), so
// repeated PR open/close cycles don't accumulate orphan
// <project>-<service>-<env>-secrets in the namespace.
//
// Resumable on partial failure: if a previous run deleted the env CR
// but errored before cleaning up addons/secrets, calling this again
// re-runs the orphan cleanup using label-based discovery instead of
// the (now-missing) env CR. The first thing every step does is
// idempotency-check; everything tolerates NotFound. Net effect: a
// caller can retry on any error without worrying about which phase
// failed.
func (s *Service) DeleteEnvironment(ctx context.Context, project, env string) error {
	return s.deleteEnvironment(ctx, project, env, false)
}

// deleteEnvironmentForce tears an env down INCLUDING a production env —
// used only by DeleteService, where deleting the whole service must also
// remove its production environment (the standalone DeleteEnvironment
// guard against deleting a lone production env doesn't apply). It performs
// the same secret / clone-PVC / volume-PVC / TLS reclaim.
func (s *Service) deleteEnvironmentForce(ctx context.Context, project, env string) error {
	return s.deleteEnvironment(ctx, project, env, true)
}

func (s *Service) deleteEnvironment(ctx context.Context, project, env string, force bool) error {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}

	// Phase 1: resolve the env CR (if it still exists) so we can pull
	// the service FQN out of its spec. On a resumed delete the CR is
	// already gone — fall back to parsing the env name. Both paths feed
	// into the same downstream cleanup.
	var (
		serviceFQN string
		envKind    string
	)
	e, gerr := s.GetEnvironment(ctx, project, env)
	switch {
	case gerr == nil:
		// Protect the TRUE production env-GROUP only. spec.kind is chart
		// semantics and is "production" on staging clones too; guarding
		// on it would wrongly make a deletable staging clone undeletable.
		// Label-first, with a spec.kind FALLBACK for legacy/hand-created
		// production envs that predate the env label — dropping the
		// fallback would make a label-less prod env wrongly deletable
		// (mirrors the web isProductionGroup helper).
		grp := e.Labels[kube.LabelEnv]
		if grp == "" {
			grp = e.Spec.Kind
		}
		if grp == "production" && !force {
			return fmt.Errorf("%w: cannot delete production environment %s", ErrInvalid, env)
		}
		serviceFQN = e.Spec.Service
		envKind = e.Spec.Kind
	case apierrors.IsNotFound(gerr) || errors.Is(gerr, ErrNotFound):
		// CR is gone — a prior run got past phase 2 but failed during
		// cleanup. Reconstruct what we can from the env name. We can't
		// verify Kind is "preview" any more, but a missing CR means
		// the prior run already passed that check, so proceeding is
		// safe.
		serviceFQN = inferServiceFQNFromEnv(env)
	default:
		return gerr
	}

	// Phase 2: delete the env CR itself. Idempotent — NotFound is OK
	// because either we just observed it missing above, or it was
	// raced away by another caller.
	if err := s.Kube.DeleteKusoEnvironment(ctx, ns, env); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete env: %w", err)
	}

	// Phase 3: tear down preview-DB clones tied to this PR. Label-based
	// discovery doesn't require the env CR to exist any more. Without
	// this, 100 PRs/day × occasional missed close-webhook = compounding
	// orphan StatefulSets + PVCs forever. Per-addon errors are tolerated
	// — the env is already gone, the addon will get reconciled on the
	// next sweep tick.
	var deletedCloneAddons []string
	if pr := previewPRNumber(env, serviceFQN); pr != "" {
		selector := kube.LabelSelector(map[string]string{
			kube.LabelProject:               project,
			"kuso.sislelabs.com/preview-pr": pr,
		})
		if addonList, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		}); lerr == nil {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					_ = derr
				} else {
					deletedCloneAddons = append(deletedCloneAddons, name)
				}
			}
		}
	} else if scope := envScopeForDelete(e, env, project, serviceFQN); scope != "" {
		// Named env (staging/qa/...): delete every addon scoped to this env via the
		// canonical env label, so the env's OWN DB/redis/s3 + their PVCs are removed.
		selector := kube.LabelSelector(map[string]string{
			kube.LabelProject: project,
			kube.LabelEnv:     scope,
		})
		if addonList, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		}); lerr == nil {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					_ = derr
				} else {
					deletedCloneAddons = append(deletedCloneAddons, name)
				}
			}
		}
	}

	// Purge the clone addons' retained data PVCs. The addon helm chart
	// stamps `helm.sh/resource-policy: keep` on the data PVC to protect
	// the PROJECT's production data on delete — but an env-scoped clone
	// (staging/qa's own throwaway DB, or a preview's per-PR DB) is meant
	// to be removed with the env. Worse, leaving the PVC behind while the
	// conn Secret is NOT retained means a same-named env recreate reuses
	// the old pgdata (old password baked into initdb) against a freshly-
	// generated conn password → SASL auth crashloop.
	//
	// This applies to previews as much as to named envs: preview clone
	// names are deterministic (`<base>-pr-<N>`), so a retained PVC from
	// PR #42 is mounted verbatim by the NEXT PR #42 in the same repo —
	// one PR's database silently readable by another. This reclaim used
	// to live inside the named-env branch only, which is exactly how
	// tickero accumulated 11 orphaned pr-* volumes still holding data.
	// Best-effort: a still-mounted PVC stamps a deletionTimestamp and
	// GCs once the StatefulSet unmounts.
	if s.Kube.Clientset != nil {
		for _, addonFQN := range deletedCloneAddons {
			pvcs, lerr := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/instance=" + addonFQN,
			})
			if lerr != nil {
				continue
			}
			for i := range pvcs.Items {
				_ = s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, pvcs.Items[i].Name, metav1.DeleteOptions{})
			}
		}

		// Reclaim the env's SERVICE volume PVCs (spec.volumes). They carry
		// helm.sh/resource-policy=keep so that REMOVING a volume from
		// spec.volumes doesn't nuke its data — but a full env DELETE should
		// reclaim them, else a same-named env recreate in the shared
		// namespace silently inherits the dead env's files (HIGH-6c). They
		// carry app.kubernetes.io/instance=<envFQN> + the volume marker
		// label; select on both so we never touch another env's disks.
		envFQN := env
		if e != nil {
			envFQN = e.Name
		}
		volPVCs, lerr := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/instance=" + envFQN + ",kuso.sislelabs.com/volume",
		})
		if lerr == nil {
			for i := range volPVCs.Items {
				_ = s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, volPVCs.Items[i].Name, metav1.DeleteOptions{})
			}
		}
	}

	// Phase 4: wipe per-env Secret. Tolerant of NotFound; the secrets
	// service handles re-entry on its own. Suppressing the error here
	// is the existing behaviour and intentional — leaving an orphan
	// Secret is preferable to surfacing an error the operator can do
	// nothing about.
	if s.SecretsCleanupForEnv != nil && serviceFQN != "" {
		svcShort := strings.TrimPrefix(serviceFQN, project+"-")
		if svcShort == "" {
			svcShort = serviceFQN
		}
		envShort := strings.TrimPrefix(env, serviceFQN+"-")
		if envShort == env {
			envShort = env
		}
		if cerr := s.SecretsCleanupForEnv(ctx, project, svcShort, envShort); cerr != nil {
			_ = cerr
		}
	}
	// Phase 5: delete the cert-manager TLS Secrets for this env. The
	// kusoenvironment Ingress only carries the cert-manager.io/cluster-issuer
	// annotation — cert-manager (not helm) creates the backing Certificate
	// CRs and the "<env>-tls" / "<env>-tls-extra-<host>" Secrets. They are
	// NOT part of the helm release, so the uninstall does not remove them and
	// (since the populated Secret has no ownerReference) nothing GCs them
	// either. Left unchecked, every env — especially every PR preview — leaks
	// a TLS Secret forever. cert-manager removes its Certificate CR on Ingress
	// teardown but leaves the Secret behind; we reclaim it here.
	if s.Kube.Clientset != nil {
		// Primary host secret is the well-known "<env>-tls".
		_ = s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, env+"-tls", metav1.DeleteOptions{})
		// Additional-host secrets are "<env>-tls-extra-<host>"; the host
		// suffix isn't known here without the env CR, so name-prefix match.
		prefix := env + "-tls-extra-"
		if secs, lerr := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{}); lerr == nil {
			for i := range secs.Items {
				if strings.HasPrefix(secs.Items[i].Name, prefix) {
					_ = s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, secs.Items[i].Name, metav1.DeleteOptions{})
				}
			}
		}
	}

	// envKind is referenced for the production-env guard above; keep
	// the variable live so future cleanup phases that need to vary by
	// kind don't have to re-fetch.
	_ = envKind
	return nil
}

// inferServiceFQNFromEnv reconstructs the service FQN when the env CR
// is already gone. Convention: env names are "<service-fqn>-<suffix>"
// where suffix is either "production" or "pr-<N>". Strip the suffix
// to get the FQN. Returns "" if neither suffix matches — in that case
// downstream label-based cleanup is a no-op (no preview-pr label to
// match) and the per-env Secret cleanup skips.
func inferServiceFQNFromEnv(env string) string {
	if i := strings.LastIndex(env, "-pr-"); i > 0 {
		return env[:i]
	}
	if strings.HasSuffix(env, "-production") {
		return strings.TrimSuffix(env, "-production")
	}
	return ""
}

// envScopeForDelete returns the env's kuso.sislelabs.com/env scope value (the env
// short name, e.g. "staging") used to find its per-env addon clones on delete.
// Prefers the CR's own env label when present; otherwise derives it from the env
// CR name ("<service-fqn>-<scope>"). Returns "" when it can't be determined (the
// addon sweep is then skipped — orphan-tolerant, like the rest of the cleanup).
func envScopeForDelete(e *kube.KusoEnvironment, env, project, serviceFQN string) string {
	if e != nil {
		if scope := e.Labels[kube.LabelEnv]; scope != "" {
			return scope
		}
	}
	if serviceFQN != "" {
		suffix := strings.TrimPrefix(env, serviceFQN+"-")
		if suffix != env && suffix != "production" {
			return suffix
		}
	}
	return ""
}

// previewPRNumber extracts the PR number from a preview env CR name.
// Convention: env name = "<service-fqn>-pr-<N>". Returns "" when
// the env isn't a preview (production envs end in "-production").
func previewPRNumber(env, serviceFQN string) string {
	suffix := strings.TrimPrefix(env, serviceFQN+"-")
	if suffix == env {
		// no service prefix on the env name; can't tell.
		return ""
	}
	if !strings.HasPrefix(suffix, "pr-") {
		return ""
	}
	return strings.TrimPrefix(suffix, "pr-")
}
