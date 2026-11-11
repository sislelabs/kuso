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
// external s3, …) must not reach the preview with its PRODUCTION conn —
// neither as an envFrom mount nor as an explicit secretKeyRef. A native
// bucket is shared through its read-only Secret only (BLOB-2), including
// for secretKeyRefs; an external one only via an explicit previewEnvVars ref.
func TestDispatch_PROpened_DropsUnclonedProductionConns(t *testing.T) {
	t.Parallel()
	prod := typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api-staging", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "staging"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "custom",
			EnvFromSecrets: []string{"alpha-db-conn", "alpha-psdb-conn", "alpha-mongo-conn", "alpha-storage-conn", "alpha-r2-conn"},
			EnvVars: []kube.KusoEnvVar{{
				Name:      "DIRECT_URL",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-psdb-conn", "key": "DIRECT_URL"}},
			}, {
				Name:      "BUCKET_KEY",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-storage-conn", "key": "S3_SECRET_ACCESS_KEY"}},
			}, {
				Name:      "R2_BUCKET",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-r2-conn", "key": "S3_BUCKET"}},
			}, {
				Name:      "R2_KEY",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-r2-conn", "key": "S3_SECRET_ACCESS_KEY"}},
			}},
		},
	})
	svc := typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api"},
		},
		Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000, Previews: &kube.KusoServicePreviews{
			PreviewEnvVars: []kube.KusoEnvVar{{
				Name:      "R2_BUCKET",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-r2-conn", "key": "S3_BUCKET"}},
			}},
		}},
	})
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		svc, prod,
		seedPreviewAddon("db", "postgres", false),
		seedPreviewAddon("psdb", "postgres", true),
		seedPreviewAddon("mongo", "mongodb", false),
		seedPreviewAddon("storage", "s3", false),
		seedPreviewAddon("r2", "s3", true),
	)
	d.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-mongo-conn", "alpha-psdb-conn", "alpha-storage-conn", "alpha-r2-conn"}, nil
	}
	d.PreviewDB = stubPreviewDB{pairs: map[string]string{"alpha-db-conn": "alpha-db-pr-7-conn"}}

	env := dispatchPR7(t, d)
	got := env.Spec.EnvFromSecrets
	for _, leak := range []string{"alpha-db-conn", "alpha-psdb-conn", "alpha-mongo-conn", "alpha-storage-conn", "alpha-r2-conn"} {
		if slices.Contains(got, leak) {
			t.Errorf("preview mounts production %s: %v", leak, got)
		}
	}
	for _, want := range []string{"alpha-db-pr-7-conn", "alpha-storage-readonly"} {
		if !slices.Contains(got, want) {
			t.Errorf("preview missing %s: %v", want, got)
		}
	}
	refs := map[string]string{}
	for _, v := range env.Spec.EnvVars {
		ref, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
		name, _ := ref["name"].(string)
		refs[v.Name] = name
	}
	want := map[string]string{"BUCKET_KEY": "alpha-storage-readonly", "R2_BUCKET": "alpha-r2-conn"}
	if len(refs) != len(want) {
		t.Errorf("preview env var refs = %v, want %v", refs, want)
	}
	for k, v := range want {
		if refs[k] != v {
			t.Errorf("preview env var refs = %v, want %v", refs, want)
		}
	}
}
