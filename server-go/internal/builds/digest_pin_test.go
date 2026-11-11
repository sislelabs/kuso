package builds

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const pinDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// BLOB-1: promotion pins the env to the digest the build pod reported, so
// a re-pushed tag can't swap the running image.
func TestPromote_PinsDigestFromBuildPod(t *testing.T) {
	t.Parallel()
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web", Kind: "production",
			Image: &kube.KusoImage{Repository: RegistryHost + "/alpha/web", Tag: "old"},
		},
	}
	s := fakeService(t, typedSeed(kube.GVREnvironments, "KusoEnvironment", env))
	ctx := context.Background()
	_, _ = s.Kube.Clientset.CoreV1().Pods("kuso").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "b1-xyz", Namespace: "kuso", Labels: map[string]string{"job-name": "b1"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "buildkit",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: "digest=" + pinDigest + "\n"}},
		}}},
	}, metav1.CreateOptions{})
	p := &Poller{Svc: s, Logger: slog.Default()}
	b := &kube.KusoBuild{ObjectMeta: metav1.ObjectMeta{Name: "b1"},
		Spec: kube.KusoBuildSpec{Image: &kube.KusoImage{Repository: RegistryHost + "/alpha/web", Tag: "new"}}}

	img := p.pinnedImage(ctx, "kuso", b)
	if img.Digest != pinDigest || img.DigestRef != RegistryHost+"/alpha/web:new" {
		t.Fatalf("pinned image = %+v", img)
	}
	if b.Spec.Image.Digest != "" {
		t.Fatal("pinnedImage mutated the build's image")
	}
	if _, err := p.promoteEnvImageCAS(ctx, "kuso", env.Name, "b1", "2026-11-11T10:00:00Z", img); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", env.Name)
	if want := RegistryHost + "/alpha/web:new@" + pinDigest; got.Spec.Image.Ref() != want {
		t.Fatalf("env image ref = %q, want %q", got.Spec.Image.Ref(), want)
	}
}

// Without a recorded digest (pod reaped, older build) the env is promoted
// by tag, exactly as before.
func TestPromote_NoDigestStaysTagOnly(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	p := &Poller{Svc: s, Logger: slog.Default()}
	b := &kube.KusoBuild{ObjectMeta: metav1.ObjectMeta{Name: "b2"},
		Spec: kube.KusoBuildSpec{Image: &kube.KusoImage{Repository: RegistryHost + "/alpha/web", Tag: "new"}}}
	img := p.pinnedImage(context.Background(), "kuso", b)
	if img.Digest != "" || img.Ref() != RegistryHost+"/alpha/web:new" {
		t.Fatalf("image = %+v ref %q", img, img.Ref())
	}
}

// A stale pin left by a merge-patch of repository/tag (rollback) must not
// apply to the new tag.
func TestImageRef_StaleDigestIgnored(t *testing.T) {
	img := kube.KusoImage{Repository: "r/a", Tag: "rolledback", Digest: pinDigest, DigestRef: "r/a:new"}
	if img.Ref() != "r/a:rolledback" {
		t.Fatalf("Ref = %q", img.Ref())
	}
}
