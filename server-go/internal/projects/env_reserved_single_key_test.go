package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// The chart renders PORT before .Values.envVars and the last duplicate
// wins, so a per-key PORT silently overrode the container port.
func TestSingleKeyEnvWrites_RejectReservedNames(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "custom", "dev", "alpha-web-staging"),
	)
	ctx := context.Background()
	for _, name := range []string{"PORT", "HOSTNAME", "KUBERNETES_SERVICE_HOST"} {
		if _, err := s.SetEnvVar(ctx, "alpha", "web", name, SetEnvVarRequest{Value: "5432"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetEnvVar(%s) err = %v, want ErrInvalid", name, err)
		}
		if _, err := s.SetEnvValue(ctx, "alpha", "web", name, "5432"); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetEnvValue(%s) err = %v, want ErrInvalid", name, err)
		}
		if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", name, SetEnvVarRequest{Value: "5432"}); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetEnvScopedVar(%s) err = %v, want ErrInvalid", name, err)
		}
	}
}
