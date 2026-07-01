package projects

import "context"

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
