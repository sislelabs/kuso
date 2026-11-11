package projects

import (
	"context"
	"testing"
)

// RUN-6: a service created now gets the TCP liveness probe on its env from
// the first render; a worker (no port, no probes) does not.
func TestAddService_NewWebServiceGetsTCPLiveness(t *testing.T) {
	t.Parallel()
	for _, rt := range []string{"dockerfile", "worker"} {
		t.Run(rt, func(t *testing.T) {
			s := defaultPodFixture(t, nil)
			req := CreateServiceRequest{Name: "web", Runtime: rt}
			if rt == "worker" {
				req.FromService = "api"
				req.Command = []string{"node", "worker.js"}
			}
			created, err := s.AddService(context.Background(), "alpha", req)
			if err != nil {
				t.Fatalf("AddService: %v", err)
			}
			want := rt != "worker"
			env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
			if err != nil {
				t.Fatalf("production env: %v", err)
			}
			if created.Spec.TCPLiveness != want || env.Spec.TCPLiveness != want {
				t.Fatalf("tcpLiveness service=%v env=%v, want %v", created.Spec.TCPLiveness, env.Spec.TCPLiveness, want)
			}
		})
	}
}
