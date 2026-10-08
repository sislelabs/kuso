package projects

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"
)

// WakeService wakes the service's production environment. See
// WakeServiceEnv.
func (s *Service) WakeService(ctx context.Context, project, service string) error {
	return s.WakeServiceEnv(ctx, project, service, "")
}

// WakeServiceEnv wakes one environment of a service. envName is the short
// env name ("production", "staging", "pr-12"), the full env CR name, or ""
// for production.
//
// It goes through scaledown.Wake — the same path the activator uses — so
// the env gets its pre-sleep replica count back, a fresh last-activity
// stamp (otherwise the next scaledown tick re-sleeps it immediately), and
// a Deployment patch, which is what reactivates an HPA env whose chart
// omits spec.replicas. A hard-stopped service or env is refused: wake must
// not silently undo `kuso stop`.
func (s *Service) WakeServiceEnv(ctx context.Context, project, service, envName string) error {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return err
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}

	fqn := service
	if !strings.HasPrefix(service, project+"-") {
		fqn = project + "-" + service
	}
	crName := envCRNameFor(project, service, "production")
	if envName != "" && envName != "production" {
		if strings.HasPrefix(envName, fqn+"-") {
			crName = envName
		} else {
			crName = envCRNameFor(project, service, envName)
		}
	}

	// A caller-supplied full CR name only has to start with "<fqn>-", which
	// another project's env can (project "a" service "b" vs project "a-b").
	env, err := s.Kube.GetOwnedEnv(ctx, ns, project, svc.Name, crName)
	if err != nil {
		if kube.IsNotFoundOrNotOwned(err) {
			return fmt.Errorf("%w: environment %s", ErrNotFound, crName)
		}
		return fmt.Errorf("get env %s: %w", crName, err)
	}
	if svc.Spec.Stopped || env.Spec.Stopped {
		return fmt.Errorf("%w: service %s/%s is stopped — start it instead of waking it", ErrConflict, project, service)
	}

	if err := scaledown.Wake(ctx, s.Kube, nil, ns, crName, time.Now()); err != nil {
		return fmt.Errorf("wake env %s: %w", crName, err)
	}

	// scaledown.Wake restores the pre-sleep count (1 when unknown); keep
	// the service's own floor too so a scale.min=3 service doesn't come
	// back at 1.
	if min := svc.Spec.Scale.MinValue(); min > 1 {
		if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, crName, func(e *kube.KusoEnvironment) error {
			if e.Spec.ReplicaCountValue() < min {
				e.Spec.SetReplicaCount(min)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("wake env %s: %w", crName, err)
		}
	}
	return nil
}
