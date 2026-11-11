package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

// BLOB-R9: a staging env that shares production's addons reads the
// production bucket through the read-only user unless it opts into rw.
func TestAddEnvironment_SharedS3IsReadOnlyByDefault(t *testing.T) {
	bucketRef := kube.KusoEnvVar{Name: "BUCKET", ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": "alpha-files-conn", "key": "S3_BUCKET"}}}
	for _, tc := range []struct {
		rw       bool
		wantConn string
	}{
		{rw: false, wantConn: "alpha-files-readonly"},
		{rw: true, wantConn: "alpha-files-conn"},
	} {
		s := fakeService(t,
			seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
			seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000, EnvVars: []kube.KusoEnvVar{bucketRef}}),
			seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
			seedAddon("alpha", "pg", "postgres"),
			seedAddon("alpha", "files", "s3"),
		)
		s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
			return []string{"alpha-pg-conn", "alpha-files-conn"}, nil
		}
		env, err := s.AddEnvironment(context.Background(), "alpha", "web",
			CreateEnvRequest{Name: "staging", Branch: "staging", ShareAddons: true, S3ReadWrite: tc.rw})
		if err != nil {
			t.Fatalf("rw=%v: %v", tc.rw, err)
		}
		other := "alpha-files-conn"
		if tc.rw {
			other = "alpha-files-readonly"
		}
		if !slices.Contains(env.Spec.EnvFromSecrets, tc.wantConn) || slices.Contains(env.Spec.EnvFromSecrets, other) ||
			!slices.Contains(env.Spec.EnvFromSecrets, "alpha-pg-conn") {
			t.Errorf("rw=%v: envFrom = %v, want %s (and the shared pg conn)", tc.rw, env.Spec.EnvFromSecrets, tc.wantConn)
		}
		var refName string
		for _, v := range env.Spec.EnvVars {
			if v.Name == "BUCKET" {
				refName, _ = v.ValueFrom["secretKeyRef"].(map[string]any)["name"].(string)
			}
		}
		if refName != tc.wantConn {
			t.Errorf("rw=%v: BUCKET reads %q, want %q", tc.rw, refName, tc.wantConn)
		}
		if addons.SharedS3ReadOnly(env.Annotations) == tc.rw {
			t.Errorf("rw=%v: annotations = %v", tc.rw, env.Annotations)
		}
	}
}
