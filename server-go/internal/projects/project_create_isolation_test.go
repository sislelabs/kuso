package projects

import (
	"context"
	"testing"
)

// ISO-2/ISO-4/ISO-11: a new project starts with env isolation on (including
// between non-production envs) and without the legacy project-wide egress
// to the shared instance Postgres.
func TestCreate_NewProjectIsIsolated(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	if _, err := s.Create(context.Background(), CreateProjectRequest{Name: "alpha"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	p, err := s.Kube.GetKusoProject(context.Background(), s.Namespace, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	np := p.Spec.NetworkPolicy
	if np == nil || np.EnvIsolation == nil || !*np.EnvIsolation ||
		np.IsolateNonProdEnvs == nil || !*np.IsolateNonProdEnvs ||
		np.InstanceDBEgress == nil || *np.InstanceDBEgress {
		t.Fatalf("new project networkPolicy = %+v", np)
	}
}
