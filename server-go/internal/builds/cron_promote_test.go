package builds

import (
	"context"
	"log/slog"
	"testing"
	"time"

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

// An older build finishing after a newer one has promoted matches no env
// (the promoted-at guard skips it) but still repointed every cron at its
// older image.
func TestPromoteImage_StaleBuildLeavesCronsAlone(t *testing.T) {
	t.Parallel()
	newer := time.Now().UTC().Format(time.RFC3339Nano)
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-production", Namespace: "kuso",
			Labels:      map[string]string{kube.LabelProject: "alpha", kube.LabelService: "web", kube.LabelEnv: "production"},
			Annotations: map[string]string{annPromotedAt: newer},
		},
		Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Branch: "main"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env),
		seedCron("alpha-web-sweeps", "alpha", "alpha-web", "service", "newtag11111"),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}
	old := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-oldtag", Namespace: "kuso",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "main",
			Image: &kube.KusoImage{Repository: "kuso-registry.kuso.svc.cluster.local:5000/alpha/web", Tag: "oldtag00000"},
		},
	}
	if err := p.promoteImage(context.Background(), "kuso", old); err != nil {
		t.Fatalf("promoteImage: %v", err)
	}
	got, err := s.Kube.GetKusoCron(context.Background(), "kuso", "alpha-web-sweeps")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != "newtag11111" {
		t.Errorf("cron image = %+v, want it left on newtag11111", got.Spec.Image)
	}
}

// seedServiceOnBranch is a service whose own repo deploys branch, which
// may differ from the project's default branch.
func seedServiceOnBranch(project, service, branch string) seed {
	return typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/example/" + service, Path: ".", DefaultBranch: branch},
		},
	})
}

func cronTag(t *testing.T, s *Service, name string) string {
	t.Helper()
	c, err := s.Kube.GetKusoCron(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get cron %s: %v", name, err)
	}
	if c.Spec.Image == nil {
		return ""
	}
	return c.Spec.Image.Tag
}

// BLD-8: the service repo deploys `master`, the project default is `main`.
// Redeploy built `main`, and a `master` build never repointed crons.
func TestServiceRepoBranchDrivesBuildAndCrons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedServiceOnBranch("alpha", "web", "master"),
		seedCron("alpha-web-sweeps", "alpha", "alpha-web", "service", "oldtag00000"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Spec.Branch != "master" {
		t.Errorf("redeploy built branch %q, want the service repo's master", got.Spec.Branch)
	}

	p := &Poller{Svc: s, Logger: slog.Default()}
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-222", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "master",
			Image: &kube.KusoImage{Repository: "reg/alpha/web", Tag: "222222222222"},
		},
	}
	if err := p.promoteToCrons(context.Background(), "kuso", b, "web"); err != nil {
		t.Fatalf("promoteToCrons: %v", err)
	}
	if tag := cronTag(t, s, "alpha-web-sweeps"); tag != "222222222222" {
		t.Errorf("cron tag = %q, want 222222222222", tag)
	}
}

// LIVE-10: `build latest` filters archived rows to services that still
// exist, and production matches each service's own deploy branch.
func TestLiveServiceBranches(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedServiceOnBranch("alpha", "api", "master"),
		seedService("beta", "web"),
	)
	got, err := s.LiveServiceBranches(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("LiveServiceBranches: %v", err)
	}
	if len(got) != 2 || got["web"] != "main" || got["api"] != "master" {
		t.Errorf("LiveServiceBranches = %v, want web=main api=master", got)
	}
}

// BLD-18: a cron on worker `jobs` (which reuses web's image) stayed on
// its creation-time image, because only the built service's crons moved.
func TestFromServicePromotionRepointsWorkerCrons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedWorkerService("alpha", "jobs", "web"),
		seedProductionEnv("alpha", "jobs"),
		seedCron("alpha-jobs-nightly", "alpha", "alpha-jobs", "service", "oldtag00000"),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-new", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "main",
			Image: &kube.KusoImage{Repository: "reg/alpha/web", Tag: "newtag11111"},
		},
	}
	bTrigger := time.Now().UTC().Format(time.RFC3339Nano)
	if err := p.promoteToFromServiceConsumers(context.Background(), "kuso", b, "web", bTrigger); err != nil {
		t.Fatalf("promoteToFromServiceConsumers: %v", err)
	}
	if tag := cronTag(t, s, "alpha-jobs-nightly"); tag != "newtag11111" {
		t.Errorf("worker cron tag = %q, want newtag11111", tag)
	}
}

// BLD-18: after rolling production back, scheduled jobs kept running the
// bad image until the next build.
func TestRollbackRepointsCrons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
		seedCron("alpha-web-sweeps", "alpha", "alpha-web", "service", "badtag00000"),
		seedCronPinned("alpha-web-pinned", "alpha", "alpha-web", "service", "pinnedtag00", true),
	)
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-oldsha", RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if tag := cronTag(t, s, "alpha-web-sweeps"); tag != "oldsha123456" {
		t.Errorf("cron tag = %q, want oldsha123456", tag)
	}
	if tag := cronTag(t, s, "alpha-web-pinned"); tag != "pinnedtag00" {
		t.Errorf("pinned cron moved to %q", tag)
	}
}
