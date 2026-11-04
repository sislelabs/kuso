package projects

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// BUILD-06: a new service whose derived names (<project>-<service> and its
// "-production" env) can't fit kube/helm name limits must be refused at
// create with a clear message, instead of producing builds and envs that
// can never be scheduled. The fqn budget (53-len("-production")) also keeps
// the build service label under 63, so it never needs clamping.
func TestAddService_RejectsNameOverDerivedBudget(t *testing.T) {
	t.Parallel()
	const project = "acme-internal-platform-services"
	s := fakeServiceWithSecrets(t, nil, seedProject(project, kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/acme/x"}}))
	_, err := s.AddService(context.Background(), project, CreateServiceRequest{Name: "notification-dispatcher-worker", Runtime: "dockerfile", Port: 8080})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("AddService err = %v, want ErrInvalid naming the length", err)
	}
	if _, gerr := s.Kube.GetKusoService(context.Background(), "kuso", project+"-notification-dispatcher-worker"); gerr == nil {
		t.Fatal("service CR was created despite the rejected name")
	}
}
