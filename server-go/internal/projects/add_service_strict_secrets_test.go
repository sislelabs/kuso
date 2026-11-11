package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// OPS-14: new services keep credential-shaped build env out of Dockerfile
// build-args by default; the caller can still opt out.
func TestAddService_StrictBuildSecretsDefaultsOn(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	ctx := context.Background()
	on, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile"})
	if err != nil {
		t.Fatal(err)
	}
	if !on.Spec.StrictBuildSecrets {
		t.Error("new service: strictBuildSecrets = false, want true by default")
	}
	no := false
	off, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "api", Runtime: "dockerfile", StrictBuildSecrets: &no})
	if err != nil {
		t.Fatal(err)
	}
	if off.Spec.StrictBuildSecrets {
		t.Error("explicit strictBuildSecrets=false ignored")
	}
}
