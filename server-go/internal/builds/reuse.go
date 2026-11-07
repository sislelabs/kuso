package builds

// Build reuse: several services in one project often build the same
// repo + path + Dockerfile at the same commit, differing only in the
// runtime command set on the Deployment. One of them builds for real;
// each identical sibling copies the resulting manifest into its OWN
// registry repo under its OWN tag (blob mount + manifest PUT, same
// digest). Because every service keeps its own repo:tag, retention,
// rollback, protection and GC need no reuse awareness.
//
// Lifecycle of a reusing build:
//   - buildcontroller.reconcile finds a source via ReuseSource and stamps
//     AnnReuseFrom instead of creating a Job.
//   - Poller.checkReuse waits for the source's Job to succeed, copies the
//     image, then takes the normal Job-succeeded path (vuln gate,
//     promotion hold, promote, release hooks, notifications).
//   - Any reuse problem removes AnnReuseFrom and stamps AnnReuseFallback,
//     and the controller then renders a Job as usual.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

const (
	// AnnReuseFrom names the same-namespace build whose image this build
	// copies instead of running its own Job.
	AnnReuseFrom = "kuso.sislelabs.com/reuse-from"
	// AnnReuseFallback records why reuse was abandoned; its presence also
	// stops the controller from choosing reuse again for this build.
	AnnReuseFallback = "kuso.sislelabs.com/reuse-fallback"

	reuseSourceMaxAge = 6 * time.Hour
	reuseMaxWait      = 2 * time.Hour
)

// ReuseEnabled reports whether the controller may choose reuse
// (KUSO_BUILD_REUSE=off disables it; builds already reusing still finish).
func ReuseEnabled() bool {
	return os.Getenv("KUSO_BUILD_REUSE") != "off"
}

type reuseKeyFields struct {
	Project    string                   `json:"project"`
	Repo       string                   `json:"repo"`
	Path       string                   `json:"path"`
	Ref        string                   `json:"ref"`
	Strategy   string                   `json:"strategy"`
	Dockerfile string                   `json:"dockerfile"`
	BuildArgs  map[string]string        `json:"buildArgs"`
	BuildEnv   map[string]string        `json:"buildEnv"`
	Static     *kube.KusoStaticSpec     `json:"static"`
	Buildpacks *kube.KusoBuildpacksSpec `json:"buildpacks"`
	PublicEnv  []string                 `json:"publicEnv"`
}

// reuseKey returns the identity of everything that determines a build's
// image bytes, or ok=false when the build can't take part in reuse.
func reuseKey(b *kube.KusoBuild) (string, bool) {
	if b == nil || b.Spec.DryRun || !shaRE.MatchString(b.Spec.Ref) || b.Spec.Repo == nil {
		return "", false
	}
	if b.Spec.Image == nil || b.Spec.Image.Tag == "" ||
		!strings.HasPrefix(b.Spec.Image.Repository, RegistryHost+"/") {
		return "", false
	}
	if b.Spec.Auth != nil && b.Spec.Auth.SecretName != "" {
		return "", false
	}
	repo := normalizePromoRepoURL(b.Spec.Repo.URL)
	if repo == "" {
		return "", false
	}
	path := b.Spec.Repo.Path
	if path == "" {
		path = "."
	}
	strategy := strings.ToLower(strings.TrimSpace(b.Spec.Strategy))
	switch strategy {
	case "nixpacks", "buildpacks", "static":
	default:
		strategy = "dockerfile"
	}
	dockerfile := strings.TrimSpace(b.Spec.Dockerfile)
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	publicEnv := slices.Clone(b.Spec.PublicEnv)
	slices.Sort(publicEnv)
	// encoding/json sorts map keys, so the encoding is canonical.
	raw, err := json.Marshal(reuseKeyFields{
		Project:    b.Spec.Project,
		Repo:       repo,
		Path:       path,
		Ref:        b.Spec.Ref,
		Strategy:   strategy,
		Dockerfile: dockerfile,
		BuildArgs:  b.Spec.BuildArgs,
		BuildEnv:   b.Spec.BuildEnv,
		Static:     b.Spec.Static,
		Buildpacks: b.Spec.Buildpacks,
		PublicEnv:  publicEnv,
	})
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

// buildCreatedBefore is the total order reuse relies on: creation time,
// then name. A build only ever reuses a strictly earlier one, so no two
// builds can wait on each other.
func buildCreatedBefore(a, b *kube.KusoBuild) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Name < b.Name
}

// ReuseSource picks the build whose image b should copy, or nil when b
// must build for real: the earliest-created live, non-reusing build of
// another service in b's namespace with the same reuse key, created in
// the last 6h and strictly before b.
func ReuseSource(b *kube.KusoBuild, candidates []*kube.KusoBuild) *kube.KusoBuild {
	if b == nil || b.Annotations[AnnReuseFallback] != "" || b.Annotations[AnnReuseFrom] != "" {
		return nil
	}
	key, ok := reuseKey(b)
	if !ok {
		return nil
	}
	cutoff := time.Now().Add(-reuseSourceMaxAge)
	var best *kube.KusoBuild
	for _, c := range candidates {
		if c == nil || c.Name == b.Name || c.Namespace != b.Namespace || c.Spec.Service == b.Spec.Service {
			continue
		}
		if c.Annotations[AnnReuseFrom] != "" || c.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		switch buildPhase(c) {
		case "failed", "cancelled", "release-failed", "queued":
			continue
		}
		if !buildCreatedBefore(c, b) {
			continue
		}
		if k, ok := reuseKey(c); !ok || k != key {
			continue
		}
		if best == nil || buildCreatedBefore(c, best) {
			best = c
		}
	}
	return best
}

// ReuseMarkPatch is the merge patch the controller applies to make b
// reuse src: the reuse annotation plus the running/started-at stamps a
// real Job would get from markRunning. The resourceVersion precondition
// makes a concurrent write (a Cancel) win.
func ReuseMarkPatch(b, src *kube.KusoBuild) []byte {
	msg := fmt.Sprintf("reusing image from %s build %s (waiting for it to build)", shortServiceName(src), src.Name)
	return []byte(fmt.Sprintf(
		`{"metadata":{"resourceVersion":%q,"annotations":{%q:%q,%q:"running",%q:%q,%q:%q}}}`,
		b.ResourceVersion, AnnReuseFrom, src.Name, annPhase,
		annStartedAt, time.Now().UTC().Format(time.RFC3339),
		annMessage, msg))
}

func shortServiceName(b *kube.KusoBuild) string {
	return strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
}

// reuseSourceReady: the source's image is pushed. annJobSucceeded, not
// phase=succeeded, because the same-repo promotion hold keeps a source
// non-terminal until its siblings — including this reuser — are green.
func reuseSourceReady(src *kube.KusoBuild) bool {
	return src.Annotations[annJobSucceeded] != "" || buildPhase(src) == "succeeded"
}

// registryRepoPath strips the in-cluster registry host off a repository.
func registryRepoPath(repository string) (string, bool) {
	p, ok := strings.CutPrefix(repository, RegistryHost+"/")
	return p, ok && p != ""
}

// checkReuse drives a build that has AnnReuseFrom and no Job: wait for the
// source, copy its image, then succeed the way a finished Job does. Every
// failure falls back to a real build; reuse never fails a build.
func (p *Poller) checkReuse(ctx context.Context, ns string, b *kube.KusoBuild) error {
	srcName := b.Annotations[AnnReuseFrom]
	src, err := p.Svc.Kube.GetKusoBuild(ctx, ns, srcName)
	if apierrors.IsNotFound(err) {
		return p.reuseFallback(ctx, ns, b, "source build "+srcName+" was deleted")
	}
	if err != nil {
		return fmt.Errorf("get reuse source %s: %w", srcName, err)
	}
	if p.ImageCopier == nil {
		return p.reuseFallback(ctx, ns, b, "registry copy is not available")
	}
	if reuseSourceReady(src) {
		if src.Spec.Image == nil || b.Spec.Image == nil {
			return p.reuseFallback(ctx, ns, b, "source or target image is not set")
		}
		srcRepo, ok1 := registryRepoPath(src.Spec.Image.Repository)
		dstRepo, ok2 := registryRepoPath(b.Spec.Image.Repository)
		if !ok1 || !ok2 {
			return p.reuseFallback(ctx, ns, b, "image is not in the in-cluster registry")
		}
		digest, cerr := p.ImageCopier.CopyImage(ctx, srcRepo, src.Spec.Image.Tag, dstRepo, b.Spec.Image.Tag)
		if cerr != nil {
			p.logger().Warn("build reuse: image copy failed; building normally",
				"build", b.Name, "source", srcName, "err", cerr)
			return p.reuseFallback(ctx, ns, b, "image copy failed: "+cerr.Error())
		}
		msg := fmt.Sprintf("reused image from %s build %s", shortServiceName(src), src.Name)
		patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annMessage, msg)
		if _, perr := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
			Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
			return fmt.Errorf("stamp reuse message: %w", perr)
		}
		if b.Annotations == nil {
			b.Annotations = map[string]string{}
		}
		b.Annotations[annMessage] = msg
		p.logger().Info("build reused sibling image", "build", b.Name, "source", srcName, "digest", digest)
		p.noteJobSucceeded(ctx, ns, b)
		return p.markSucceededAsync(ctx, ns, b)
	}
	switch ph := buildPhase(src); ph {
	case "failed", "cancelled", "release-failed":
		return p.reuseFallback(ctx, ns, b, "source build "+srcName+" "+ph)
	}
	if src.Spec.Done || src.Labels[LabelBuildState] == BuildStateDone {
		return p.reuseFallback(ctx, ns, b, "source build "+srcName+" finished without an image")
	}
	waited := buildAge(b)
	if t, perr := time.Parse(time.RFC3339, b.Annotations[annStartedAt]); perr == nil {
		waited = time.Since(t)
	}
	if waited > reuseMaxWait {
		return p.reuseFallback(ctx, ns, b, "source build "+srcName+" not ready within "+reuseMaxWait.String())
	}
	return nil
}

// reuseFallback hands the build back to the controller for a real Job.
// dispatched-at restarts the no-Job stuck clock (buildAge reads it), or a
// build that waited past the stuck-timeout would be force-failed before
// its Job renders.
func (p *Poller) reuseFallback(ctx context.Context, ns string, b *kube.KusoBuild, reason string) error {
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:null,%q:%q,%q:"pending",%q:null,%q:null,%q:%q}}}`,
		AnnReuseFrom, AnnReuseFallback, reason, annPhase, annStartedAt, annMessage,
		annDispatchedAt, time.Now().UTC().Format(time.RFC3339))
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("build reuse fallback: %w", err)
	}
	p.logger().Info("build reuse abandoned; building normally", "build", b.Name, "reason", reason)
	return nil
}
