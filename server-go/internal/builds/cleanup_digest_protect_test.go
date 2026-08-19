package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestSweepImagesPastWindow_ProtectsSharedDigests is the regression for
// H3: protection was TAG-based but registry deletion is DIGEST-based.
// DeleteImageTag resolves the tag to its manifest digest and DELETEs
// manifests/<digest> — which removes the manifest for EVERY tag pointing
// at it. Two byte-identical builds (same commit rebuilt) share a digest,
// so untagging an aged-out unprotected tag used to kill the manifest a
// protected live env tag depended on → the env's next pod restart was a
// permanent ImagePullBackOff even though its own tag was "protected".
func TestSweepImagesPastWindow_ProtectsSharedDigests(t *testing.T) {
	t.Parallel()

	seedB := func(name, tag string, sec int64) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso",
				CreationTimestamp: metav1.NewTime(time.Unix(sec, 0)),
				Labels:            map[string]string{"kuso.sislelabs.com/build-state": "done"},
				Annotations:       map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: tag},
			},
		})
	}

	// The production env runs "livetag" — tag-protected.
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-production", Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "web",
				"kuso.sislelabs.com/env":     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web", Kind: "production",
			Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: "livetag0000"},
		},
	}

	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env),
		seedB("alpha-web-live", "livetag0000", 900), // newest, in window, live on env
		seedB("alpha-web-dup", "duptag00000", 500),  // aged out — but SAME DIGEST as livetag
		seedB("alpha-web-old", "uniqtag0000", 100),  // aged out, unique digest → must be swept
	)

	del := &fakeDeleter{digests: map[string]string{
		// duptag is a byte-identical rebuild of livetag: one manifest,
		// two tags. Deleting duptag's manifest would 404 livetag too.
		"alpha/web:livetag0000": "sha256:shared-aa",
		"alpha/web:duptag00000": "sha256:shared-aa",
		"alpha/web:uniqtag0000": "sha256:unique-bb",
	}}

	// keep=1 → duptag + uniqtag both fall out of the window.
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	deleted := map[string]bool{}
	for _, d := range del.deleted {
		deleted[d] = true
	}
	if deleted["alpha/web:duptag00000"] {
		t.Errorf("sweep deleted a tag whose manifest digest is shared with the LIVE env tag — "+
			"the registry DELETE is digest-scoped, so this 404s the protected image too (deleted=%v)", del.deleted)
	}
	if deleted["alpha/web:livetag0000"] {
		t.Errorf("sweep deleted the live env tag itself: %v", del.deleted)
	}
	// Non-vacuous: the sweep must still untag the digest-unique aged-out
	// image.
	if !deleted["alpha/web:uniqtag0000"] {
		t.Errorf("sweep skipped the unshared aged-out tag; deleted=%v — test would pass on a no-op sweep", del.deleted)
	}
}

// TestSweepImagesPastWindow_ProtectedDigestResolveFailsClosed: when a
// protected tag's digest can't be resolved, the sweep must abort rather
// than guess — without the digest view it cannot know which manifests
// are load-bearing (same fail-closed rule as the env/cron CR lists).
func TestSweepImagesPastWindow_ProtectedDigestResolveFailsClosed(t *testing.T) {
	t.Parallel()

	seedB := func(name, tag string, sec int64) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso",
				CreationTimestamp: metav1.NewTime(time.Unix(sec, 0)),
				Labels:            map[string]string{"kuso.sislelabs.com/build-state": "done"},
				Annotations:       map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: tag},
			},
		})
	}
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web", Kind: "production",
			Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: "livetag0000"},
		},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env),
		seedB("alpha-web-live", "livetag0000", 900),
		seedB("alpha-web-old", "oldtag00000", 100),
	)

	del := &erroringResolveDeleter{}
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err == nil {
		t.Fatal("sweep must fail closed when a protected tag's digest can't be resolved")
	}
	if len(del.deleted) != 0 {
		t.Errorf("sweep deleted %v despite failing to resolve protected digests", del.deleted)
	}
}

// erroringResolveDeleter fails every digest resolution — models the
// registry being unreachable mid-sweep.
type erroringResolveDeleter struct{ deleted []string }

func (f *erroringResolveDeleter) DeleteImageTag(_ context.Context, repo, tag string) error {
	f.deleted = append(f.deleted, repo+":"+tag)
	return nil
}

func (f *erroringResolveDeleter) ResolveTagDigest(context.Context, string, string) (string, error) {
	return "", context.DeadlineExceeded
}
