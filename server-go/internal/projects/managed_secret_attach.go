package projects

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// attachServiceSecretToEnvs adds the managed <project>-<service>-secrets
// Secret to spec.envFromSecrets on every NON-preview env of the service
// that doesn't already mount it. Previews are excluded on purpose — the
// same fresh-slate rule secrets.attachToAllEnvs applies.
//
// This is the missing half of the unified env write (the koreni bug):
// upsertManagedSecretKey stored values in the Secret while assuming the
// mount already existed on the env CRs — but nothing ever created it, so
// pods silently ran without the vars. Idempotent; call on every secret
// write.
func (s *Service) attachServiceSecretToEnvs(ctx context.Context, ns, project, service string) error {
	secretName := kube.ServiceSecretName(project, service)
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		return fmt.Errorf("list envs for secret attach: %w", err)
	}
	for i := range envs {
		env := &envs[i]
		if env.Spec.Kind == "preview" {
			continue
		}
		if hasEnvFromSecret(env.Spec.EnvFromSecrets, secretName) {
			continue
		}
		// RMW, not a merge patch of the full array: a concurrent writer
		// (addon provision appending a clone-conn, AddEnvironment) that
		// lands between our List and the write must not get its entry
		// clobbered by our stale snapshot. Mutating the LIVE object and
		// letting updateWithRetry re-read on conflict keeps both edits.
		if _, uerr := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, env.Name, func(live *kube.KusoEnvironment) error {
			if !hasEnvFromSecret(live.Spec.EnvFromSecrets, secretName) {
				live.Spec.EnvFromSecrets = append(live.Spec.EnvFromSecrets, secretName)
			}
			return nil
		}); uerr != nil {
			return fmt.Errorf("attach %s to env %s: %w", secretName, env.Name, uerr)
		}
	}
	return nil
}

func hasEnvFromSecret(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

// HealManagedSecretMounts is the boot-time sweep for services that hit
// the pre-fix window: their managed <service>-secrets Secret exists (the
// unified write created it) but no env CR mounts it, so every literal
// env var is silently absent from the pods. For each project/service
// whose managed Secret exists, re-run the idempotent attach. Best-effort:
// failures are logged and don't block boot.
func (s *Service) HealManagedSecretMounts(ctx context.Context, logger *slog.Logger) {
	if s.Kube == nil || s.Kube.Clientset == nil {
		return
	}
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		if logger != nil {
			logger.Warn("managed-secret heal: list projects failed", "err", err)
		}
		return
	}
	healed := 0
	for i := range projects {
		project := projects[i].Name
		ns, nerr := s.namespaceFor(ctx, project)
		if nerr != nil {
			if logger != nil {
				logger.Warn("managed-secret heal: resolve namespace failed — project skipped unhealed",
					"project", project, "err", nerr)
			}
			continue
		}
		services, serr := s.ListServices(ctx, project)
		if serr != nil {
			if logger != nil {
				logger.Warn("managed-secret heal: list services failed", "project", project, "err", serr)
			}
			continue
		}
		for j := range services {
			svcShort := services[j].Labels[labelService]
			if svcShort == "" {
				continue
			}
			secretName := kube.ServiceSecretName(project, svcShort)
			if _, gerr := s.Kube.Clientset.CoreV1().Secrets(ns).
				Get(ctx, secretName, metav1.GetOptions{}); gerr != nil {
				if !apierrors.IsNotFound(gerr) && logger != nil {
					logger.Warn("managed-secret heal: read secret failed",
						"project", project, "service", svcShort, "err", gerr)
				}
				continue // no managed Secret → nothing to mount
			}
			if aerr := s.attachServiceSecretToEnvs(ctx, ns, project, svcShort); aerr != nil {
				if logger != nil {
					logger.Warn("managed-secret heal: attach failed",
						"project", project, "service", svcShort, "err", aerr)
				}
				continue
			}
			// Second half of the heal: a literal on an env CR SHADOWS the
			// managed Secret (inline `env` beats `envFrom`), so a key
			// present in both served its stale CR value while `env list`
			// reported the new one. Writes clear this going forward; this
			// sweep fixes services already desynced.
			if n, cerr := s.clearShadowedEnvLiterals(ctx, ns, project, svcShort, secretName); cerr != nil {
				if logger != nil {
					logger.Warn("managed-secret heal: clear shadowed literals failed",
						"project", project, "service", svcShort, "err", cerr)
				}
			} else if n > 0 && logger != nil {
				logger.Info("managed-secret heal: cleared shadowing env literals",
					"project", project, "service", svcShort, "keys", n)
			}
			healed++
		}
	}
	if logger != nil && healed > 0 {
		logger.Info("managed-secret heal: swept services with managed secrets", "services", healed)
	}
}

// clearShadowedEnvLiterals removes env-CR literals whose name also exists as
// a key in the service's managed Secret. Such a literal wins over the
// envFrom mount, so the pod serves the OLD value while the API reports the
// new one — the desync that made a GitHub OAuth secret rotation silently
// no-op. Only plain literals are cleared: a valueFrom entry is an explicit
// wiring the user chose, not an accident of a superseded write.
// Returns how many keys were cleared.
func (s *Service) clearShadowedEnvLiterals(ctx context.Context, ns, project, service, secretName string) (int, error) {
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil || len(sec.Data) == 0 {
		return 0, nil
	}
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		return 0, err
	}
	cleared := 0
	for i := range envs {
		var shadowed []string
		for _, ev := range envs[i].Spec.EnvVars {
			if ev.ValueFrom != nil {
				continue // explicit wiring — leave alone
			}
			if _, inSecret := sec.Data[ev.Name]; inSecret {
				shadowed = append(shadowed, ev.Name)
			}
		}
		if len(shadowed) == 0 {
			continue
		}
		drop := make(map[string]bool, len(shadowed))
		for _, n := range shadowed {
			drop[n] = true
		}
		if _, uerr := s.updateOwnedEnvWithRetry(ctx, ns, project, envs[i].Name, func(env *kube.KusoEnvironment) error {
			out := env.Spec.EnvVars[:0]
			for _, ev := range env.Spec.EnvVars {
				if ev.ValueFrom != nil || !drop[ev.Name] {
					out = append(out, ev)
				}
			}
			env.Spec.EnvVars = out
			return nil
		}); uerr != nil {
			return cleared, fmt.Errorf("clear shadowed literals on env %s: %w", envs[i].Name, uerr)
		}
		cleared += len(shadowed)
	}
	return cleared, nil
}
