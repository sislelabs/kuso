package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

func TestPatchService_WatchPaths(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile"}),
	)
	ctx := context.Background()

	set := []string{"apps/web/**", " packages/ui/** "}
	got, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{WatchPaths: &set})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spec.WatchPaths) != 2 || got.Spec.WatchPaths[1] != "packages/ui/**" {
		t.Errorf("watchPaths = %q", got.Spec.WatchPaths)
	}

	// Omitting the field leaves it alone.
	port := int32(9000)
	got, err = s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Port: &port})
	if err != nil || len(got.Spec.WatchPaths) != 2 {
		t.Errorf("unrelated patch touched watchPaths: %q, %v", got.Spec.WatchPaths, err)
	}

	clear := []string{}
	got, err = s.PatchService(ctx, "alpha", "web", PatchServiceRequest{WatchPaths: &clear})
	if err != nil || len(got.Spec.WatchPaths) != 0 {
		t.Errorf("empty list must clear: %q, %v", got.Spec.WatchPaths, err)
	}

	for _, bad := range [][]string{{"../secrets/**"}, {"apps/[web"}, {"apps/web/../../x"}} {
		b := bad
		if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{WatchPaths: &b}); !errors.Is(err, ErrInvalid) {
			t.Errorf("watchPaths %q: err = %v, want ErrInvalid", bad, err)
		}
	}
}

func regcredSecret(project, host string) runtime.Object {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: project + "-regcred-" + host, Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: project, kube.LabelPrefix + "registry-credential": "true"},
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}
}

func TestPatchService_ImagePullSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t,
		[]runtime.Object{regcredSecret("alpha", "ghcr-io"), regcredSecret("beta", "ghcr-io")},
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "image",
			Image:   &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "v1"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	ctx := context.Background()

	ref := "ghcr.io"
	got, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Image: &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v1", PullSecret: &ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Image.PullSecret != "alpha-regcred-ghcr-io" {
		t.Errorf("service pullSecret = %q, want the resolved Secret name", got.Spec.Image.PullSecret)
	}
	env := envByName(t, s, "alpha", "web")["alpha-web-production"]
	if env.Spec.Image == nil || env.Spec.Image.PullSecret != "alpha-regcred-ghcr-io" {
		t.Errorf("pullSecret did not reach the env CR: %+v", env.Spec.Image)
	}

	// A tag bump that omits pullSecret keeps it.
	got, err = s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Image: &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v2"},
	})
	if err != nil || got.Spec.Image.PullSecret != "alpha-regcred-ghcr-io" || got.Spec.Image.Tag != "v2" {
		t.Errorf("tag bump dropped pullSecret: %+v, %v", got.Spec.Image, err)
	}

	// Another project's credential is not resolvable.
	foreign := "beta-regcred-ghcr-io"
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Image: &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v2", PullSecret: &foreign},
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign credential: err = %v, want ErrNotFound", err)
	}

	empty := ""
	got, err = s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Image: &ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v2", PullSecret: &empty},
	})
	if err != nil || got.Spec.Image.PullSecret != "" {
		t.Errorf("empty pullSecret must clear: %+v, %v", got.Spec.Image, err)
	}
}

func TestAddService_StampsWatchPathsAndPullSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t,
		[]runtime.Object{regcredSecret("alpha", "ghcr-io")},
		seedProject("alpha", kube.KusoProjectSpec{}),
	)
	ref := "ghcr.io"
	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "api", Runtime: "image",
		Image:      &ServiceImageSpec{Repository: "ghcr.io/acme/api", Tag: "v1", PullSecret: &ref},
		WatchPaths: []string{"apps/api/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Spec.Image == nil || created.Spec.Image.PullSecret != "alpha-regcred-ghcr-io" {
		t.Errorf("image = %+v", created.Spec.Image)
	}
	if len(created.Spec.WatchPaths) != 1 || created.Spec.WatchPaths[0] != "apps/api/**" {
		t.Errorf("watchPaths = %q", created.Spec.WatchPaths)
	}
	env := envByName(t, s, "alpha", "api")["alpha-api-production"]
	if env.Spec.Image == nil || env.Spec.Image.PullSecret != "alpha-regcred-ghcr-io" {
		t.Errorf("production env image = %+v", env.Spec.Image)
	}
}
