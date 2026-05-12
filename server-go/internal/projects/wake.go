package projects

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// WakeService nudges a sleeping service awake by patching its
// production environment's spec.replicas back up. Sleep state is
// driven by the operator's HPA scale-to-zero — we just bump the
// desired count so the next reconcile loop wakes the deployment.
//
// Behaviour:
//   - If the production env is already running (replicas > 0), this is
//     a no-op (returns nil).
//   - Replica count is set to the service's spec.scale.min (default 1).
//   - Returns ErrNotFound if the service or its production env doesn't
//     exist.
func (s *Service) WakeService(ctx context.Context, project, service string) error {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}

	// Compose the production env CR name following the existing convention:
	// <project>-<service>-production.
	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}
	envName := fqn + "-production"

	envCR, err := s.Kube.GetKusoEnvironment(ctx, ns, envName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("get production env: %w", err)
	}

	min := 1
	if svc.Spec.Scale != nil && svc.Spec.Scale.Min > 0 {
		min = svc.Spec.Scale.Min
	}

	// Stamp desired replicas onto status.wakeReplicas — the operator's
	// reconciler reads this hint when waking. We don't touch spec to
	// avoid stomping the user's last-known scale.
	if envCR.Status == nil {
		envCR.Status = map[string]any{}
	}
	envCR.Status["wakeReplicas"] = min
	envCR.Status["wakeRequestedAt"] = nowRFC3339()

	if _, err := s.Kube.UpdateKusoEnvironment(ctx, ns, envCR); err != nil {
		return fmt.Errorf("patch env wake hint: %w", err)
	}
	return nil
}
