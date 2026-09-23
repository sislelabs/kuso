package addons

import (
	"context"
	"slices"
	"testing"
)

// A project addon whose name extends a subscribed one ("storage-archive" vs
// "storage") is a different addon, not a clone. Mounting it on an env that
// subscribes only to "storage" put its conn after the real one, and envFrom
// is last-source-wins, so production resolved S3_BUCKET to the archive.
func TestRefresh_SiblingAddonWithSubscribedPrefixIsNotMounted(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("tk"),
		seedLabeledAddon("tk", "storage", "s3"),
		seedLabeledAddon("tk", "storage-archive", "s3"),
		seedEnvWithSecrets("tk", "api", "production", "tk-api-production",
			[]string{"storage"},
			[]string{"tk-storage-conn", "tk-api-secrets"},
		),
	)
	if _, err := s.Add(context.Background(), "tk", CreateAddonRequest{Name: "cache", Kind: "redis"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tk-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	got := env.Spec.EnvFromSecrets
	if slices.Contains(got, "tk-storage-archive-conn") {
		t.Errorf("production env mounts the unsubscribed sibling tk-storage-archive-conn: %v", got)
	}
	if !slices.Contains(got, "tk-storage-conn") {
		t.Errorf("production env lost its subscribed tk-storage-conn: %v", got)
	}
}
