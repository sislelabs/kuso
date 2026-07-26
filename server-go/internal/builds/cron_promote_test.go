package builds

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedCron(name, project, service, kind, tag string) seed {
	return seedCronPinned(name, project, service, kind, tag, false)
}

func seedCronPinned(name, project, service, kind, tag string, pin bool) seed {
	return typedSeed(kube.GVRCrons, "KusoCron", &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project:  project,
			Service:  service,
			Kind:     kind,
			PinImage: pin,
			Image: &kube.KusoImage{
				Repository: "kuso-registry.kuso.svc.cluster.local:5000/" + project + "/" + service,
				Tag:        tag,
			},
		},
	})
}

// TestPromoteToCrons_RepointsInheritedImage is the regression for the
// scubatony-internal-system-daily-sweeps failure of 2026-07-26.
//
// A cron snapshots the production env's image at creation time and, pre-
// fix, only re-resolved on an explicit `kuso cron sync`. Every deploy
// left it one build further behind. The drift was invisible while the
// old tag still existed — then the weekly registry GC reaped it and the
// CronJob began failing ImagePullBackOff on every fire, emitting a
// pod.crashed alert every ~25 minutes while the service itself was
// perfectly healthy on a newer tag.
func TestPromoteToCrons_RepointsInheritedImage(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		// Inherits the service image — must be repointed.
		//
		// spec.service carries the FULLY-QUALIFIED name here, which is
		// what crons.Add actually writes. The first version of this test
		// seeded the SHORT form and passed against a buggy
		// implementation that only compared the short name — the real
		// cron on the cluster never matched. Keep the FQN case first.
		seedCron("alpha-web-sweeps", "alpha", "alpha-web", "service", "oldtag00000"),
		// Short form too: hand-written CRs and older records use it.
		seedCron("alpha-web-sweeps-short", "alpha", "web", "service", "oldtag00000"),
		// kind=command carries its OWN image — must NOT be touched.
		seedCron("alpha-web-standalone", "alpha", "web", "command", "pinned-v1"),
		// Different service — out of scope.
		seedCron("alpha-api-sweeps", "alpha", "api", "service", "othertag000"),
		// Explicitly pinned — the opt-out. Must NOT be repointed.
		seedCronPinned("alpha-web-pinned", "alpha", "alpha-web", "service", "pinnedtag00", true),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}

	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-newtag", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "main",
			Image: &kube.KusoImage{
				Repository: "kuso-registry.kuso.svc.cluster.local:5000/alpha/web",
				Tag:        "newtag11111",
			},
		},
	}
	if err := p.promoteToCrons(context.Background(), "kuso", b, "web"); err != nil {
		t.Fatalf("promoteToCrons: %v", err)
	}

	for _, tc := range []struct{ cron, want string }{
		{"alpha-web-sweeps", "newtag11111"},       // repointed (FQN spec.service)
		{"alpha-web-sweeps-short", "newtag11111"}, // repointed (short spec.service)
		{"alpha-web-standalone", "pinned-v1"},     // kind=command untouched
		{"alpha-api-sweeps", "othertag000"},       // other service untouched
		{"alpha-web-pinned", "pinnedtag00"},       // pinImage=true opts out
	} {
		got, err := s.Kube.GetKusoCron(context.Background(), "kuso", tc.cron)
		if err != nil {
			t.Fatalf("get %s: %v", tc.cron, err)
		}
		if got.Spec.Image == nil || got.Spec.Image.Tag != tc.want {
			t.Errorf("%s: image tag = %v, want %q", tc.cron, got.Spec.Image, tc.want)
		}
	}
}
