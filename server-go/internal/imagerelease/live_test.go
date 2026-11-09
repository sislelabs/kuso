package imagerelease

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func TestRolledOut(t *testing.T) {
	t.Parallel()
	two := int32(2)
	dep := func(img string, ready, updated, total int32) *appsv1.Deployment {
		d := &appsv1.Deployment{}
		d.Spec.Replicas = &two
		d.Spec.Template.Spec.Containers = []corev1.Container{{Image: img}}
		d.Status = appsv1.DeploymentStatus{ReadyReplicas: ready, UpdatedReplicas: updated, Replicas: total}
		return d
	}
	cases := []struct {
		name string
		d    *appsv1.Deployment
		want bool
	}{
		{"all ready on the image", dep("nginx:1.27", 2, 2, 2), true},
		{"pull never succeeded", dep("nginx:nope", 0, 1, 3), false},
		{"old pods still around", dep("nginx:1.27", 2, 2, 3), false},
		{"different image", dep("nginx:1.28", 2, 2, 2), false},
		{"no deployment", nil, false},
	}
	for _, c := range cases {
		if got := rolledOut(c.d, "nginx:1.27"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// The live-images record lands right after a rollout finishes. Written
// under the "kuso-server" field manager it read to drift as a fresh spec
// edit and kept "Applying changes…" up for two minutes after the
// service was live, so it must go out under its own manager.
func TestAddLiveImage_UsesAnnotationFieldManager(t *testing.T) {
	t.Parallel()
	kc := fakeKube(t, seedEnv("shop-web-production", kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-web", Kind: "production"}))
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	if err := addLiveImage(context.Background(), kc, "kuso", "shop-web-production", "nginx:1.27", now); err != nil {
		t.Fatal(err)
	}
	var patched bool
	for _, a := range kc.Dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
		p, ok := a.(k8stesting.PatchActionImpl)
		if !ok {
			continue
		}
		patched = true
		if p.PatchOptions.FieldManager != FieldManagerAnnotations {
			t.Errorf("field manager = %q, want %q", p.PatchOptions.FieldManager, FieldManagerAnnotations)
		}
	}
	if !patched {
		t.Fatal("no patch sent")
	}
	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "shop-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if got := parseLiveImages(env.Annotations[AnnLiveImages]); len(got.Refs) != 1 || got.Refs[0] != "nginx:1.27" || got.Since != "2026-11-09T12:00:00Z" {
		t.Errorf("live images = %+v", got)
	}
}
