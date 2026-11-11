package builds

import (
	"context"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

var terminationDigestRE = regexp.MustCompile(`(?m)^digest=(sha256:[0-9a-f]{64})$`)

// pinnedImage returns the build's image with Digest/DigestRef set, so the
// env runs the exact manifest this build pushed and a later re-push of
// the tag can't swap it (BLOB-1). The digest comes from the build pod's
// termination message (the buildkit script writes what it pushed); when
// the pod is gone or older than this code, the in-cluster registry is
// asked for the tag's current digest. Any failure returns the image
// unpinned, which runs by tag as before.
func (p *Poller) pinnedImage(ctx context.Context, ns string, b *kube.KusoBuild) *kube.KusoImage {
	if b.Spec.Image == nil || b.Spec.Image.Repository == "" || b.Spec.Image.Tag == "" {
		return b.Spec.Image
	}
	img := *b.Spec.Image
	img.Digest, img.DigestRef = "", ""
	digest := p.buildPodDigest(ctx, ns, b.Name)
	if digest == "" && p.Svc.Images != nil && strings.HasPrefix(img.Repository, RegistryHost+"/") {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		dg, err := p.Svc.Images.ResolveTagDigest(rctx, strings.TrimPrefix(img.Repository, RegistryHost+"/"), img.Tag)
		cancel()
		if err != nil {
			p.logger().Warn("promote: could not resolve image digest; promoting by tag", "build", b.Name, "err", err)
		}
		digest = dg
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return &img
	}
	img.Digest = digest
	img.DigestRef = img.TagRef()
	return &img
}

// lazyPinnedImage defers pinnedImage until an env actually takes the
// image, then memoises it.
func (p *Poller) lazyPinnedImage(ctx context.Context, ns string, b *kube.KusoBuild) func() *kube.KusoImage {
	var img *kube.KusoImage
	return func() *kube.KusoImage {
		if img == nil {
			img = p.pinnedImage(ctx, ns, b)
		}
		return img
	}
}

// buildPodDigest reads "digest=sha256:..." from the buildkit container's
// termination message of the build Job's pod, or "".
func (p *Poller) buildPodDigest(ctx context.Context, ns, jobName string) string {
	if p.Svc.Kube == nil || p.Svc.Kube.Clientset == nil {
		return ""
	}
	pods, err := p.Svc.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return ""
	}
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			if cs.Name != "buildkit" || cs.State.Terminated == nil || cs.State.Terminated.ExitCode != 0 {
				continue
			}
			if m := terminationDigestRE.FindStringSubmatch(cs.State.Terminated.Message); m != nil {
				return m[1]
			}
		}
	}
	return ""
}
