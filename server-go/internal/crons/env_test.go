package crons

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// A service cron got only envFromSecrets, never the env's envVars, so
// ${{ addon.KEY }} aliases and env-CR literals were missing and the job
// ran on app defaults (live: scubatony daily-sweeps without DATABASE_URL
// alias, OPS_S3_*, NODE_ENV).
func TestAdd_CarriesProductionEnvVars(t *testing.T) {
	t.Parallel()
	s := egressFixture(t, false, false)
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "alpha-db-conn", "key": "DATABASE_URL"}}
	if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(context.Background(), "kuso", "alpha-web-production", func(e *kube.KusoEnvironment) error {
		e.Spec.EnvVars = []kube.KusoEnvVar{
			{Name: "NODE_ENV", Value: "production"},
			{Name: "DATABASE_URI", ValueFrom: ref},
			{Name: "API_KEY", Source: "managed-secret"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cr, err := s.Add(context.Background(), "alpha", "web", CreateCronRequest{
		Name: "nightly", Schedule: "0 0 * * *", Command: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := map[string]kube.KusoRunEnv{}
	for _, e := range cr.Spec.Env {
		got[e.Name] = e
	}
	if got["NODE_ENV"].Value != "production" || got["DATABASE_URI"].ValueFrom == nil {
		t.Fatalf("cron env = %+v", cr.Spec.Env)
	}
	if _, ok := got["API_KEY"]; ok {
		t.Error("managed-secret placeholder copied; envFrom already supplies it")
	}
}
