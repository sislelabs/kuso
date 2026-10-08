package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// An env-group clone's env is named <p>-<svc>-<group>-production but
// labelled env=<group>. Rename must carry the label, not re-derive it from
// the CR name: env=production makes the addon refresh mount production's
// conns on the qa env.
func TestRenameService_EnvGroupCloneKeepsEnvLabel(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "alpha", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	before, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-qa-production")
	if err != nil {
		t.Fatalf("get clone env: %v", err)
	}
	if _, err := s.RenameService(ctx, "alpha", "web-qa", "site-qa"); err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-site-qa-production")
	if err != nil {
		t.Fatalf("get renamed env: %v", err)
	}
	if got := env.Labels[labelEnv]; got != "qa" {
		t.Errorf("env label = %q, want qa", got)
	}
	if got := env.Labels[labelService]; got != "site-qa" {
		t.Errorf("service label = %q, want site-qa", got)
	}
	for k, v := range before.Annotations {
		if env.Annotations[k] != v {
			t.Errorf("annotation %s = %q, want %q", k, env.Annotations[k], v)
		}
	}
}
