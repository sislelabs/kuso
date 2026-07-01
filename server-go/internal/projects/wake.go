package projects

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

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

	// Confirm the production env exists (maps to ErrNotFound for the
	// handler); the RMW below re-reads it for the actual mutation.
	if _, err := s.Kube.GetKusoEnvironment(ctx, ns, envName); err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("get production env: %w", err)
	}

	// Wake always brings replicas to at least 1 — even if the user
	// asked for scale-to-zero, a manual wake means they want it running
	// now.
	min := svc.Spec.Scale.MinValue()
	if min < 1 {
		min = 1
	}

	// Scale the env back up via spec.replicaCount — the exact inverse of
	// scaledown's SetReplicaCount(0), and what the kusoenvironment chart's
	// Deployment reads (`replicas: {{ .Values.replicaCount }}`).
	//
	// The previous implementation stamped status.wakeReplicas and did a
	// plain Update — but KusoEnvironment.status is a SUBRESOURCE, so the
	// main-resource Update silently dropped the status write, AND nothing
	// (operator chart or server) ever read wakeReplicas. So wake was a
	// no-op that returned success. Use the RMW helper (avoids a stale-read
	// overwrite) and write spec, which actually reconciles.
	if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(e *kube.KusoEnvironment) error {
		if e.Spec.ReplicaCountValue() < min {
			e.Spec.SetReplicaCount(min)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("wake env %s: %w", envName, err)
	}
	return nil
}
