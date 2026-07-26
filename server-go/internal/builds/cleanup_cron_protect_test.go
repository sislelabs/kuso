package builds

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// fakeDeleter records what the sweep tried to untag.
type fakeDeleter struct{ deleted []string }

func (f *fakeDeleter) DeleteImageTag(_ context.Context, repo, tag string) error {
	f.deleted = append(f.deleted, repo+":"+tag)
	return nil
}

// TestSweepImagesPastWindow_ProtectsCronImages is the regression for the
// ROOT CAUSE of the scubatony-internal-system-daily-sweeps outage.
//
// The sweep keeps the newest N builds per service and protects anything
// a live KusoEnvironment is running — but it never looked at crons. A
// cron carries its own copy of an image tag, and a PINNED cron sits
// still while its service keeps building, so it falls out of the
// newest-N window faster than anything else. The sweep untagged
// 9bca1d03de79 while the cron still referenced it and every subsequent
// fire was an unrecoverable ImagePullBackOff.
func TestSweepImagesPastWindow_ProtectsCronImages(t *testing.T) {
	t.Parallel()
	old := metav1.NewTime(time.Unix(100, 0))
	newer := metav1.NewTime(time.Unix(900, 0))

	seedB := func(name, tag string, at metav1.Time) seed {
		return typedSeed(kube.GVRBuilds, "KusoBuild", &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "kuso", CreationTimestamp: at,
				Labels:      map[string]string{"kuso.sislelabs.com/build-state": "done"},
				Annotations: map[string]string{annPhase: "succeeded"},
			},
			Spec: kube.KusoBuildSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{
					Repository: "reg.local:5000/alpha/web", Tag: tag,
				},
			},
		})
	}

	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		// The cron still references the OLD tag — exactly the shape that
		// broke in production. Note spec.service is the FQN (what
		// crons.Add writes) while the image REPOSITORY uses the short
		// service name, matching the build's — that is what production
		// actually stores, verified on the live cluster.
		typedSeed(kube.GVRCrons, "KusoCron", &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-sweeps", Namespace: "kuso"},
			Spec: kube.KusoCronSpec{
				Project: "alpha", Service: "alpha-web", Kind: "service",
				Image: &kube.KusoImage{
					Repository: "reg.local:5000/alpha/web", Tag: "oldtag00000",
				},
			},
		}),
		seedB("alpha-web-old", "oldtag00000", old),
		seedB("alpha-web-new", "newtag11111", newer),
	)

	del := &fakeDeleter{}
	// keep=1 → the old build is outside the window and would be untagged
	// were it not protected by the cron reference.
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	for _, d := range del.deleted {
		if d == "alpha/web:oldtag00000" {
			t.Errorf("sweep untagged an image a cron still references (%s) — the next cron fire would be a permanent ImagePullBackOff", d)
		}
	}
}

// TestSweepImagesPastWindow_ProtectsPinnedCronImages covers the case the
// protection exists FOR. A pinned cron (pinImage=true) is deliberately
// skipped by promoteToCrons, so it keeps referencing an older tag than
// any live env and falls out of the newest-N window sooner than
// anything else on the cluster — it is the single most likely thing to
// be swept.
//
// The KusoCronSpec.PinImage doc comment promises pinning is safe
// *because* the sweep treats cron-referenced images as in-use. This
// test is what makes that promise true rather than assumed.
func TestSweepImagesPastWindow_ProtectsPinnedCronImages(t *testing.T) {
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

	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		// Pinned: promoteToCrons skips it, so it stays on the ancient tag.
		typedSeed(kube.GVRCrons, "KusoCron", &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-pinned", Namespace: "kuso"},
			Spec: kube.KusoCronSpec{
				Project: "alpha", Service: "alpha-web", Kind: "service", PinImage: true,
				Image: &kube.KusoImage{Repository: "reg.local:5000/alpha/web", Tag: "ancient0000"},
			},
		}),
		seedB("alpha-web-ancient", "ancient0000", 100),
		seedB("alpha-web-mid", "midtag00000", 500),
		seedB("alpha-web-new", "newtag11111", 900),
	)

	del := &fakeDeleter{}
	// keep=1 → both older builds are outside the window; only the
	// pinned cron's tag must survive.
	if _, err := SweepImagesPastWindow(context.Background(), s.Kube, "kuso", nil, del, 1, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, d := range del.deleted {
		if d == "alpha/web:ancient0000" {
			t.Fatalf("sweep untagged a PINNED cron's image (%s) — pinImage would rot into ImagePullBackOff, "+
				"breaking the guarantee KusoCronSpec.PinImage documents", d)
		}
	}
	// Sanity: the sweep must still be doing its job on unreferenced tags,
	// otherwise this test would pass on a no-op sweep.
	var sweptMid bool
	for _, d := range del.deleted {
		if d == "alpha/web:midtag00000" {
			sweptMid = true
		}
	}
	if !sweptMid {
		t.Errorf("sweep did not untag the unreferenced mid tag; deleted=%v — test would pass vacuously", del.deleted)
	}
}
