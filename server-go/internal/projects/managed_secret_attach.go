package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
		next, jerr := json.Marshal(append(env.Spec.EnvFromSecrets, secretName))
		if jerr != nil {
			return fmt.Errorf("marshal envFromSecrets for %s: %w", env.Name, jerr)
		}
		patch := fmt.Sprintf(`{"spec":{"envFromSecrets":%s}}`, next)
		if _, perr := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, env.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			return fmt.Errorf("attach %s to env %s: %w", secretName, env.Name, perr)
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
			healed++
		}
	}
	if logger != nil && healed > 0 {
		logger.Info("managed-secret heal: swept services with managed secrets", "services", healed)
	}
}
