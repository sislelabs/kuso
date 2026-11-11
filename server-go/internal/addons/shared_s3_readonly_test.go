package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestSwapToReadOnlyS3(t *testing.T) {
	t.Parallel()
	swaps := map[string]string{"p-files-conn": "p-files-readonly"}
	none := func(string) bool { return false }
	for _, tc := range []struct {
		in      []string
		allowed func(string) bool
		want    []string
	}{
		{[]string{"p-pg-conn", "p-files-conn"}, nil, []string{"p-pg-conn", "p-files-readonly"}},
		// Already swapped and the rw conn re-added by the subscription pass.
		{[]string{"p-files-readonly", "p-files-conn"}, nil, []string{"p-files-readonly"}},
		// Unsubscribed: the read-only mount goes with it.
		{[]string{"p-pg-conn", "p-files-readonly"}, none, []string{"p-pg-conn"}},
	} {
		if got := SwapToReadOnlyS3(tc.in, swaps, tc.allowed); !slices.Equal(got, tc.want) {
			t.Errorf("SwapToReadOnlyS3(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// BLOB-R9: an addon event rebuilds every env's mounts from the project's
// read-write conns; a read-only staging env must not come back read-write,
// while an older staging env without the annotation keeps its rw mount.
func TestRefreshEnvSecrets_KeepsSharedS3ReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-files", Namespace: "kuso", Labels: map[string]string{kube.LabelProject: "alpha"}},
			Spec:       kube.KusoAddonSpec{Project: "alpha", Kind: "s3"},
		}),
		seedEnv("alpha", "web", "staging", "alpha-web-staging"),
		seedEnv("alpha", "api", "staging", "alpha-api-staging"),
	)
	ro, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-staging")
	if err != nil {
		t.Fatal(err)
	}
	ro.Annotations = map[string]string{AnnotationSharedS3ReadOnly: "true"}
	ro.Spec.EnvFromSecrets = []string{"alpha-files-readonly"}
	if _, err := s.Kube.UpdateKusoEnvironment(ctx, "kuso", ro); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshEnvSecrets(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"alpha-web-staging": "alpha-files-readonly", "alpha-api-staging": "alpha-files-conn"} {
		got, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", name)
		other := map[string]string{"alpha-files-readonly": "alpha-files-conn", "alpha-files-conn": "alpha-files-readonly"}[want]
		if !slices.Contains(got.Spec.EnvFromSecrets, want) || slices.Contains(got.Spec.EnvFromSecrets, other) {
			t.Errorf("%s envFrom = %v, want %s", name, got.Spec.EnvFromSecrets, want)
		}
	}
}
