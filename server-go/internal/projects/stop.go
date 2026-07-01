package projects

import (
	"context"
	"fmt"
	"strings"

	"kuso/server/internal/kube"
)

// StopService hard-stops a service: sets spec.stopped=true, which pins
// every owned environment to 0 replicas AND tells the activator NOT to
// wake it on traffic. Unlike sleep (auto scale-to-zero, woken by the
// next request), a stopped service stays down until StartService is
// called. Visitors get a "service stopped" 503 in the meantime.
//
// Implemented on top of PatchService so it reuses the single
// service→env propagation chokepoint (the env CRs the operator +
// activator read are updated in lockstep). Idempotent: stopping an
// already-stopped service is a no-op patch.
func (s *Service) StopService(ctx context.Context, project, service string) error {
	stopped := true
	_, err := s.PatchService(ctx, project, service, PatchServiceRequest{Stopped: &stopped})
	return err
}

// StartService clears the hard-stop (spec.stopped=false). The operator
// then scales the deployment back to its configured replica count on
// the next reconcile, and the activator resumes normal wake-on-traffic
// behaviour. Idempotent.
func (s *Service) StartService(ctx context.Context, project, service string) error {
	stopped := false
	_, err := s.PatchService(ctx, project, service, PatchServiceRequest{Stopped: &stopped})
	return err
}

// StopProject hard-stops EVERY service in a project. Best-effort: it
// tries all services and returns a combined error naming any that
// failed, rather than aborting on the first — a partial stop is still
// better than none, and the caller (UI) can re-run to retry stragglers.
// Idempotent per service.
func (s *Service) StopProject(ctx context.Context, project string) error {
	return s.setProjectStopped(ctx, project, true)
}

// StartProject clears the hard-stop on every service in a project.
// Best-effort with the same combined-error semantics as StopProject.
func (s *Service) StartProject(ctx context.Context, project string) error {
	return s.setProjectStopped(ctx, project, false)
}

func (s *Service) setProjectStopped(ctx context.Context, project string, stopped bool) error {
	svcs, err := s.ListServices(ctx, project)
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	verb := "start"
	if stopped {
		verb = "stop"
	}
	var failed []string
	for i := range svcs {
		// ListServices returns FQN names (<project>-<service>); the
		// per-service methods take the short name and re-prefix, so strip
		// the project prefix here to avoid double-prefixing.
		short := strings.TrimPrefix(svcs[i].Name, project+"-")
		var perr error
		if stopped {
			perr = s.StopService(ctx, project, short)
		} else {
			perr = s.StartService(ctx, project, short)
		}
		if perr != nil {
			failed = append(failed, short)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s failed for %d service(s): %s", verb, len(failed), strings.Join(failed, ", "))
	}
	return nil
}

// envSleepFrom maps a service's sleep spec to the env's slimmer sleep
// spec (the kusoenvironment chart only reads sleep.enabled, to route the
// ingress at the activator for scale-to-zero). Nil / disabled → nil, so
// the env's ingress points at the app's own Service. Shared by the env-
// creation sites and the propagation chokepoint so they can't diverge.
func envSleepFrom(s *kube.KusoServiceSleep) *kube.KusoEnvSleep {
	if s == nil || !s.Enabled {
		return nil
	}
	return &kube.KusoEnvSleep{Enabled: true}
}
