// service → env propagation chokepoint.
//
// Why this file exists, separately from services_ops.go and
// projects_ops.go:
//
// The kusoenvironment helm chart reads only the env CR; the parent
// service CR is invisible to it. So every service-level edit
// (PatchService, SetEnv, AddDomain, …) has to mirror the changed
// fields onto every env CR or the change never reaches a running pod.
// That mirror logic is the single highest-leverage chokepoint in the
// codebase — a bug here silently breaks every save flow in the UI.
//
// Pulling it into its own file makes that chokepoint structurally
// visible: any change to service → env mirroring lives here, period.
// `propagateChangedToEnvs` is the single entrypoint for service-level
// field changes; `propagateBaseDomain` is the project-level analogue.
//
// The functions stay methods on *Service because they need its Kube
// client, home namespace, and the existing per-service mutex — the
// extraction is about file-level scoping, not changing the type
// hierarchy.

package projects

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"kuso/server/internal/kube"
)

// changedFields names which fields on the parent KusoService have
// been edited in a single PatchService call. propagateChangedToEnvs
// dispatches on this so it only writes the env CRs once (instead of
// the previous O(fields × envs) updates), even when several fields
// flip together.
type changedFields struct {
	EnvVars   bool
	Placement bool
	Volumes   bool
	Port      bool
	Scale     bool
	Domains   bool
	Internal  bool
	// Runtime carries spec.runtime changes (e.g. dockerfile→worker).
	// The chart renders ports + probes + Service + Ingress conditionally
	// on the env's runtime, so without this propagation a runtime flip
	// at the service level was silently dropped — the deployment kept
	// its old probe shape forever and crashlooped headless binaries.
	Runtime bool
	// PrivateEgress carries spec.privateEgress changes. The chart
	// stamps the public-egress pod label off the env CR's value, so a
	// service-level toggle that isn't propagated never reaches a pod.
	PrivateEgress bool
	// Release carries spec.release changes. The build poller reads
	// it off the env CR (which is already in its hot-path GET) when
	// deciding whether to run a release Job before promoting an image.
	Release bool
}

func (c changedFields) any() bool {
	return c.EnvVars || c.Placement || c.Volumes || c.Port || c.Scale || c.Domains || c.Internal || c.Runtime || c.PrivateEgress || c.Release
}

// propagateChangedToEnvs is the single chokepoint that mirrors a
// post-PATCH KusoService onto every owned KusoEnvironment. Replaces
// six per-field propagators that each issued their own LIST + N
// UPDATEs; this version lists envs once and applies every changed
// field in one Update per env.
//
// Long-term fix is to teach the chart to merge both CRs; until then
// this is the single place to keep correct.
func (s *Service) propagateChangedToEnvs(ctx context.Context, ns, project, service string, svc *kube.KusoService, changed changedFields) error {
	if !changed.any() {
		return nil
	}
	// Cached typed list — warm informer = slice filter, cold = one
	// network call.
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		return fmt.Errorf("list envs for propagation: %w", err)
	}
	// Debug: surface the count + names so we can tell from the logs
	// whether propagation actually walked both envs of a service.
	{
		names := make([]string, 0, len(envs))
		for i := range envs {
			names = append(names, envs[i].Name)
		}
		slog.InfoContext(ctx, "propagate: envs matched",
			"project", project, "service", service,
			"count", len(envs), "envs", names,
			"changed_envvars", changed.EnvVars)
	}
	// Resolve the effective placement once before the loop when
	// Placement is changed — calling GetKusoProject inside the loop
	// produces N+1 apiserver GETs for a service with N envs. The
	// project spec doesn't change during propagation, so one fetch
	// suffices. Best-effort: a project-fetch error falls back to
	// copying the raw service placement.
	var effectivePlacement *kube.KusoPlacement
	if changed.Placement {
		if proj, perr := s.Kube.GetKusoProject(ctx, s.Namespace, project); perr == nil {
			effectivePlacement = ResolvePlacement(proj.Spec.Placement, svc.Spec.Placement)
		} else {
			effectivePlacement = svc.Spec.Placement
		}
	}
	for i := range envs {
		envName := envs[i].Name
		// RMW with retry-on-409: re-fetch the env CR, apply the
		// propagation, write. Without this the helm-operator's
		// status patches (which fire every reconcile while the env
		// is rolling) race our update — the apiserver returns 409,
		// the legacy update() path bumps RV on our STALE snapshot
		// and resends, overwriting any spec field the operator
		// touched. F-03 fixed the same class for KusoService writes;
		// the propagation loop is the last surface in this package
		// that needs RMW.
		_, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(env *kube.KusoEnvironment) error {
			if changed.EnvVars {
				// Per-key shared-secret subscription: when the parent
				// service has spec.sharedEnvKeys set (non-nil), expand
				// each key into a valueFrom.secretKeyRef and merge with
				// the service's explicit envVars. nil sharedEnvKeys =
				// legacy chart-blanket-mount path; we pass through
				// untouched. The env CR also mirrors the subscription
				// list so the dashboard's per-env view can read it
				// without re-resolving the service spec.
				env.Spec.SharedEnvKeys = svc.Spec.SharedEnvKeys
				env.Spec.SubscribedAddons = svc.Spec.SubscribedAddons
				// Rewrite service-ref literals to the env-scoped form.
				// svc.Spec.EnvVars carries production-scoped literals
				// (set by buildServiceResolver at SetEnv time); per-
				// env propagation re-targets them so a staging-scoped
				// envVar lands at <fqn>-staging.<ns>.svc.cluster.local,
				// not the production sibling (B4.1/B4.2 from v0.17.0
				// audit).
				envScope := env.Labels[labelEnv]
				if envScope == "" {
					envScope = "production"
				}
				rescopedSvcEnvVars := rescopeServiceRefLiterals(svc.Spec.EnvVars, ns, envScope)
				merged, prunedFrom, err := s.resolveSharedEnvKeysForEnv(
					ctx, ns, project,
					svc.Spec.SharedEnvKeys,
					rescopedSvcEnvVars,
					env.Spec.EnvVars, // preserve per-env overrides (R-bug)
					env.Spec.EnvFromSecrets,
				)
				if err != nil {
					return fmt.Errorf("resolve sharedEnvKeys for env %s: %w", envName, err)
				}
				env.Spec.EnvVars = merged
				// Filter the propagated envFromSecrets by the addon
				// subscription. nil SubscribedAddons = legacy auto-
				// mount-all (no change); non-nil = only addons in the
				// list keep their conn-secret mount.
				if svc.Spec.SubscribedAddons != nil {
					projectAddons := s.listProjectAddonConnSecrets(ctx, project)
					prunedFrom = filterEnvFromForSubscription(prunedFrom, svc.Spec.SubscribedAddons, projectAddons, project)
				}
				env.Spec.EnvFromSecrets = prunedFrom
			}
			if changed.Placement {
				env.Spec.Placement = effectivePlacement
			}
			if changed.Volumes {
				env.Spec.Volumes = svc.Spec.Volumes
			}
			if changed.Port {
				port := svc.Spec.Port
				if port == 0 {
					port = 8080
				}
				env.Spec.Port = port
			}
			if changed.Scale {
				auto := autoscalingFromScale(svc.Spec.Scale)
				env.Spec.SetReplicaCount(effectiveScaleMin(svc))
				env.Spec.Autoscaling = auto
			}
			if changed.Domains {
				// Custom domains are now per-env (env.Spec.AdditionalHosts).
				// Stop overwriting them from the service-level template;
				// otherwise a Networking save on production silently
				// clobbers staging's custom domains (or worse, makes
				// staging start serving tickero.bg → Ingress conflict
				// with production).
				//
				// The chart still reads env.Spec.AdditionalHosts as the
				// source of truth. To edit per-env hosts, go through
				// the env-scoped PATCH endpoint (or the dashboard's
				// Networking section, which is bound to the env CR
				// post-v0.16.19). The service-level spec.domains field
				// is now only used as a seed template at AddEnvironment
				// time.
				_ = svc
			}
			if changed.Internal {
				env.Spec.Internal = svc.Spec.Internal
			}
			if changed.Runtime {
				env.Spec.Runtime = svc.Spec.Runtime
			}
			if changed.PrivateEgress {
				env.Spec.PrivateEgress = svc.Spec.PrivateEgress
			}
			if changed.Release {
				env.Spec.Release = svc.Spec.Release
			}
			return nil
		})
		if err != nil {
			slog.WarnContext(ctx, "propagate: update env failed",
				"env", envName, "err", err)
			return fmt.Errorf("update env %s: %w", envName, err)
		}
		slog.InfoContext(ctx, "propagate: env updated", "env", envName)
	}
	return nil
}

// propagateBaseDomain rewrites the Host on every owned env that still
// holds the OLD default-shape host (i.e. the auto-generated domain we
// stamped at create time). Hosts that don't match the old pattern were
// customised by the user and are left alone — overwriting them would
// silently destroy the operator's custom DNS work.
//
// AdditionalHosts (manually-added domains in the Networking tab) are
// untouched. Only the primary Host moves.
func (s *Service) propagateBaseDomain(ctx context.Context, project, oldBase, newBase string) error {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
	for i := range envs {
		env := &envs[i]
		expected := defaultHost(env.Spec.Service, project, oldBase)
		if env.Spec.Host != expected {
			// User-customised host — leave it.
			continue
		}
		// RMW retry so a status-patch race doesn't lose the new
		// host while the helm-operator status-patches in parallel.
		if _, uerr := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, env.Name, func(live *kube.KusoEnvironment) error {
			live.Spec.Host = defaultHost(env.Spec.Service, project, newBase)
			live.Spec.TLSHosts = computeTLSHosts(live.Spec.Host, live.Spec.AdditionalHosts)
			return nil
		}); uerr != nil {
			return fmt.Errorf("update env %s: %w", env.Name, uerr)
		}
	}
	return nil
}

// rescopeServiceRefLiterals walks every envVar value looking for the
// in-cluster DNS form "<fqn>-production.<ns>.svc.cluster.local" and
// rewrites the "-production" segment to the target env's short name.
// Production envs (envScope=="production") pass through unchanged.
// Worker envs / non-production envs get their staging-scoped sibling.
//
// Why string-substitute instead of re-resolving via the resolver:
// the resolver requires the project's service list + each env's
// host data which the propagation hot-loop doesn't carry. The
// production-scoped literal already stored on svc.Spec.EnvVars
// follows a deterministic shape (set by ExpandServiceKey), so a
// targeted regex rewrite is safe and cheap.
func rescopeServiceRefLiterals(in []kube.KusoEnvVar, ns, envScope string) []kube.KusoEnvVar {
	if envScope == "" || envScope == "production" || len(in) == 0 {
		return in
	}
	suffix := ".svc.cluster.local"
	prodSeg := "-production." + ns + suffix
	envSeg := "-" + envScope + "." + ns + suffix
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		if e.Value == "" || e.ValueFrom != nil {
			out[i] = e
			continue
		}
		if !strings.Contains(e.Value, prodSeg) {
			out[i] = e
			continue
		}
		copy := e
		copy.Value = strings.ReplaceAll(e.Value, prodSeg, envSeg)
		out[i] = copy
	}
	return out
}
