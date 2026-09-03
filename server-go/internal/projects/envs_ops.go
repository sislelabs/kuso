package projects

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
// When PreviewPROpen is wired, an expired env is only deleted after
// GitHub confirms its PR is no longer open. An OPEN PR gets its
// expiresAt re-stamped (project ttlDays) instead — the TTL alone is
// not evidence of abandonment, just of no pushes: tickero PR 46 sat
// quietly for 7 days while still open and under review, and the sweep
// silently tore its preview down (2026-08-06). A check error keeps
// the env for this tick; the 5-minute ticker retries. Without the
// checker (nil, e.g. no GitHub App configured) the legacy
// delete-on-expiry behaviour is unchanged.
//
// Returns the number of envs deleted. Errors against individual envs
// are logged via the supplied callback (or swallowed when nil) so one
// flaky teardown doesn't stop the sweep.
//
// previewPRCheckGraceCap bounds how long a check-error can defer the
// delete: past this far beyond expiresAt the failure is treated as
// permanent (App uninstalled, repo transferred — it retries every 5
// minutes, so 14 days ≈ 4000 consecutive failures) and the env is
// deleted anyway. An open PR under active review re-stamps its TTL on
// every push, so a genuinely live preview doesn't sit weeks past
// expiry. Without the cap an uninstalled App leaked the preview env +
// DB clone + PVC forever.
const previewPRCheckGraceCap = 14 * 24 * time.Hour

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
	// ttlDaysByProject feeds the open-PR extension below — re-stamp
	// with the same TTL the dispatcher would use, not a hardcoded one.
	ttlDaysByProject := make(map[string]int, len(projects))
	for _, p := range projects {
		if p.Spec.Previews != nil && p.Spec.Previews.TTLDays > 0 {
			ttlDaysByProject[p.Name] = p.Spec.Previews.TTLDays
		}
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
			proj := e.Spec.Project
			if proj == "" {
				proj = e.Labels[kube.LabelProject]
			}
			// PR-state gate: an expired preview whose PR is still OPEN is
			// not abandoned — extend its TTL instead of deleting. Only a
			// confirmed closed/merged/vanished PR falls through to the
			// delete. Check errors keep the env for this tick (the ticker
			// retries in 5 minutes); deleting an active PR's preview on a
			// transient GitHub error is strictly worse than a short leak.
			if s.PreviewPROpen != nil && proj != "" {
				if prNum, ok := previewPRNumberFromEnv(&e); ok {
					open, cerr := s.PreviewPROpen(ctx, proj, prNum)
					if cerr != nil {
						// Transient failures keep the env (retry next tick).
						// But a PERMANENTLY failing check — GitHub App
						// uninstalled, repo transferred — must not leak the
						// preview forever: past the grace cap the env has
						// been expired AND unanswerable for so long that an
						// active PR would have re-stamped the TTL by pushing.
						// Fall through to the delete at that point.
						if now.Sub(exp) < previewPRCheckGraceCap {
							if onErr != nil {
								onErr(e.Name, fmt.Errorf("pr-state check: %w", cerr))
							}
							continue
						}
						if onErr != nil {
							onErr(e.Name, fmt.Errorf("pr-state check failing %s past expiry — grace cap exceeded, deleting: %w",
								now.Sub(exp).Round(time.Hour), cerr))
						}
					} else if open {
						ttlDays := ttlDaysByProject[proj]
						if ttlDays <= 0 {
							ttlDays = 7
						}
						newExp := now.Add(time.Duration(ttlDays) * 24 * time.Hour).Format(time.RFC3339)
						if _, uerr := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, e.Name, func(live *kube.KusoEnvironment) error {
							if live.Spec.TTL == nil {
								live.Spec.TTL = &kube.KusoTTL{}
							}
							live.Spec.TTL.ExpiresAt = newExp
							return nil
						}); uerr != nil && onErr != nil {
							onErr(e.Name, fmt.Errorf("extend ttl: %w", uerr))
						}
						continue
					}
				}
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

// previewPRNumberFromEnv extracts the PR number a preview env belongs to.
// Primary source is the env-group label (kuso.sislelabs.com/env =
// "preview-pr-N" — stamped by ensurePreviewEnv); fallback is the CR
// name's "-pr-N" suffix for hand-created / pre-label CRs. Returns
// ok=false when neither yields a number, in which case the sweep
// falls back to legacy delete-on-expiry.
func previewPRNumberFromEnv(e *kube.KusoEnvironment) (int, bool) {
	if v, ok := strings.CutPrefix(e.Labels[kube.LabelEnv], "preview-pr-"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, true
		}
	}
	if i := strings.LastIndex(e.Name, "-pr-"); i >= 0 {
		if n, err := strconv.Atoi(e.Name[i+len("-pr-"):]); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
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
		// CR is gone. Two very different situations reach here:
		//
		//  1. A RESUMED delete — a prior run got past phase 2 but failed
		//     during cleanup, leaving orphaned addons/PVCs/Secrets behind.
		//     Proceeding is correct and is the whole reason this branch
		//     exists.
		//  2. A name that was never an env CR at all — most commonly an
		//     env GROUP name ("preview-pr-52", which spans one CR per
		//     service: tickero-api-pr-52, tickero-worker-pr-52, …). That
		//     belongs to DeleteEnvGroup.
		//
		// Case 2 used to fall through here and report SUCCESS while
		// deleting no env CR whatsoever — a false success on a destructive
		// command. Worse, it still ran the label-based addon cascade, so
		// `kuso project env delete tickero preview-pr-52` tore down the
		// per-PR DATABASE (matched by the preview-pr label) while leaving
		// all four env CRs running, then printed "deleted" (2026-09-03).
		//
		// Tell them apart by asking whether this name is an env GROUP: a
		// group has one env CR PER SERVICE, all carrying the group's name
		// in the env label. If a LIVE list finds any, the caller wants
		// DeleteEnvGroup and this function would delete none of them while
		// still cascading the addons — so refuse and say where to go.
		//
		// Name-shape heuristics are NOT enough here and were tried first:
		// inferServiceFQNFromEnv("preview-pr-52") returns "preview" (a
		// plausible-looking but nonexistent service), while a genuine
		// resumed delete of "tickero-api-staging" returns "" because the
		// scope isn't a recognised suffix. Both directions are wrong. The
		// label list is the only authority.
		groupSel := kube.LabelSelector(map[string]string{
			kube.LabelProject: project,
			kube.LabelEnv:     env,
		})
		if grp, lerr := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			List(ctx, metav1.ListOptions{LabelSelector: groupSel}); lerr == nil && len(grp.Items) > 0 {
			names := make([]string, 0, len(grp.Items))
			for i := range grp.Items {
				names = append(names, grp.Items[i].GetName())
			}
			return fmt.Errorf("%w: %q is an env GROUP spanning %d environments (%s) — delete it as a group, not as a single environment",
				ErrInvalid, env, len(names), strings.Join(names, ", "))
		}
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

	// Cleanup-failure collection. The env CR is already gone by phase 3,
	// so a swallowed failure below is a FALSE SUCCESS that orphans a
	// PVC/Secret/addon invisibly — the exact class behind the v0.21.6
	// preview-PVC leak. Contract: NotFound stays ignored (deletes are
	// idempotent/resumable), any other failure is logged with enough
	// context to find the orphan AND collected; the cascade keeps going
	// past it, and the join is returned at the end.
	var cleanupErrs []error
	cleanupFail := func(kind, name string, err error) {
		slog.Warn("env delete: cleanup step failed; resource may be orphaned",
			"project", project, "env", env, "ns", ns, "kind", kind, "name", name, "err", err)
		cleanupErrs = append(cleanupErrs, fmt.Errorf("%s %s/%s: %w", kind, ns, name, err))
	}

	// Phase 3: tear down preview-DB clones tied to this PR. Label-based
	// discovery doesn't require the env CR to exist any more. Without
	// this, 100 PRs/day × occasional missed close-webhook = compounding
	// orphan StatefulSets + PVCs forever. Per-addon failures don't abort
	// the cascade, but they are collected — see cleanupFail above.
	// DESTRUCTIVE-CASCADE ENUMERATION — both branches issue a LIVE LIST
	// (raw dynamic client), never the informer cache: an addon created
	// seconds before the delete may not have reached the cache yet, and a
	// cold/degraded informer returns empty-but-ok — either silently skips
	// the reclaim and orphans a StatefulSet+PVC+live credentials (the
	// v0.21.6/v0.21.7 leak class). Cached helpers stay on READ paths only.
	var deletedCloneAddons []string
	if pr := previewPRNumber(env, serviceFQN); pr != "" {
		selector := kube.LabelSelector(map[string]string{
			kube.LabelProject:               project,
			"kuso.sislelabs.com/preview-pr": pr,
		})
		if addonList, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		}); lerr != nil {
			cleanupFail("KusoAddonList", selector, lerr)
		} else {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					cleanupFail("KusoAddon", name, derr)
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
		}); lerr != nil {
			cleanupFail("KusoAddonList", selector, lerr)
		} else {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					cleanupFail("KusoAddon", name, derr)
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
				cleanupFail("PersistentVolumeClaimList", "app.kubernetes.io/instance="+addonFQN, lerr)
				continue
			}
			for i := range pvcs.Items {
				if derr := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, pvcs.Items[i].Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
					cleanupFail("PersistentVolumeClaim", pvcs.Items[i].Name, derr)
				}
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
		if lerr != nil {
			cleanupFail("PersistentVolumeClaimList", "app.kubernetes.io/instance="+envFQN, lerr)
		} else {
			for i := range volPVCs.Items {
				if derr := s.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, volPVCs.Items[i].Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
					cleanupFail("PersistentVolumeClaim", volPVCs.Items[i].Name, derr)
				}
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
			// Deliberately tolerated (not collected): per the contract above,
			// an orphan per-env Secret is preferable to failing the delete —
			// but it must at least be visible in the logs.
			slog.Warn("env delete: per-env secret cleanup failed (tolerated)",
				"project", project, "env", env, "service", svcShort, "err", cerr)
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
		if derr := s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, env+"-tls", metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			cleanupFail("Secret", env+"-tls", derr)
		}
		// Additional-host secrets are "<env>-tls-extra-<host>"; the host
		// suffix isn't known here without the env CR, so name-prefix match.
		prefix := env + "-tls-extra-"
		if secs, lerr := s.Kube.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{}); lerr != nil {
			cleanupFail("SecretList", prefix+"*", lerr)
		} else {
			for i := range secs.Items {
				if strings.HasPrefix(secs.Items[i].Name, prefix) {
					if derr := s.Kube.Clientset.CoreV1().Secrets(ns).Delete(ctx, secs.Items[i].Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
						cleanupFail("Secret", secs.Items[i].Name, derr)
					}
				}
			}
		}
	}

	// envKind is referenced for the production-env guard above; keep
	// the variable live so future cleanup phases that need to vary by
	// kind don't have to re-fetch.
	_ = envKind
	// Surface collected cleanup failures. The env CR itself is gone (the
	// delete is resumable — re-running takes the CR-already-gone path and
	// re-attempts this cleanup), so the error is "deleted with orphans",
	// not "delete failed".
	if len(cleanupErrs) > 0 {
		return fmt.Errorf("env %s deleted, but cleanup left orphans: %w", env, errors.Join(cleanupErrs...))
	}
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
