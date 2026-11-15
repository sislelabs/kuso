package imagerelease

import (
	"context"
	"testing"
	"time"

	"log/slog"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
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

// A digest-pinned env renders "repo:tag@sha256:…" into the Deployment.
// Matching the bare "repo:tag" against that never succeeded, so no build
// promoted with a digest was ever recorded and every one read as "never
// ready". The record itself stays bare: the UI keys builds by repo:tag.
func TestRecordLiveImage_DigestPinnedEnv(t *testing.T) {
	t.Parallel()
	img := &kube.KusoImage{Repository: "reg/shop-web", Tag: "abc123", Digest: "sha256:feed", DigestRef: "reg/shop-web:abc123"}
	kc := fakeKube(t, seedEnv("shop-web-production", kube.KusoEnvironmentSpec{Project: "shop", Service: "shop-web", Kind: "production", Image: img}))
	two := int32(2)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "shop-web-production", Namespace: "kuso"}}
	dep.Spec.Replicas = &two
	dep.Spec.Template.Spec.Containers = []corev1.Container{{Image: "reg/shop-web:abc123@sha256:feed"}}
	dep.Status = appsv1.DeploymentStatus{ReadyReplicas: 2, UpdatedReplicas: 2, Replicas: 2}
	kc.Clientset = k8sfake.NewClientset(dep)

	w := &Watcher{Kube: kc, Namespace: "kuso", Logger: slog.Default()}
	env, err := kc.GetKusoEnvironment(context.Background(), "kuso", "shop-web-production")
	if err != nil {
		t.Fatal(err)
	}
	w.recordLiveImage(context.Background(), "kuso", env)

	env, err = kc.GetKusoEnvironment(context.Background(), "kuso", "shop-web-production")
	if err != nil {
		t.Fatal(err)
	}
	if got := parseLiveImages(env.Annotations[AnnLiveImages]); len(got.Refs) != 1 || got.Refs[0] != "reg/shop-web:abc123" {
		t.Errorf("live images = %+v, want [reg/shop-web:abc123]", got)
	}
}
