package github

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedPreviewAddon(short, kind string, external bool) seed {
	spec := kube.KusoAddonSpec{Project: "alpha", Kind: kind}
	if external {
		spec.External = &kube.KusoAddonExternal{SecretName: short + "-src"}
	}
	return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-" + short, Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha"},
		},
		Spec: spec,
	})
}

// DATA-03: an addon the preview has no clone of (external postgres, mongodb,
// …) must not reach the preview with its PRODUCTION conn — neither as an
// envFrom mount nor as an explicit secretKeyRef. s3 stays shared by design.
func TestDispatch_PROpened_DropsUnclonedProductionConns(t *testing.T) {
	t.Parallel()
	prod := typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api-production", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "production"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "production",
			EnvFromSecrets: []string{"alpha-db-conn", "alpha-psdb-conn", "alpha-mongo-conn", "alpha-storage-conn"},
			EnvVars: []kube.KusoEnvVar{{
				Name:      "DIRECT_URL",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-psdb-conn", "key": "DIRECT_URL"}},
			}},
		},
	})
	svc := typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api"},
		},
		Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000},
	})
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		svc, prod,
		seedPreviewAddon("db", "postgres", false),
		seedPreviewAddon("psdb", "postgres", true),
		seedPreviewAddon("mongo", "mongodb", false),
		seedPreviewAddon("storage", "s3", false),
	)
	d.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-mongo-conn", "alpha-psdb-conn", "alpha-storage-conn"}, nil
	}
	d.PreviewDB = stubPreviewDB{pairs: map[string]string{"alpha-db-conn": "alpha-db-pr-7-conn"}}

	env := dispatchPR7(t, d)
	got := env.Spec.EnvFromSecrets
	for _, leak := range []string{"alpha-db-conn", "alpha-psdb-conn", "alpha-mongo-conn"} {
		if slices.Contains(got, leak) {
			t.Errorf("preview mounts production %s: %v", leak, got)
		}
	}
	for _, want := range []string{"alpha-db-pr-7-conn", "alpha-storage-conn"} {
		if !slices.Contains(got, want) {
			t.Errorf("preview missing %s: %v", want, got)
		}
	}
	for _, v := range env.Spec.EnvVars {
		if v.Name == "DIRECT_URL" {
			t.Errorf("preview keeps a secretKeyRef into the production external DB: %+v", v)
		}
	}
}
