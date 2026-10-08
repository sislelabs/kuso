package builds

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedStartedBuild(name, project, service string, annotations map[string]string) seed {
	return seedBuild(&kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels:      map[string]string{kube.LabelProject: project, kube.LabelService: project + "-" + service},
			Annotations: annotations,
		},
		Spec: kube.KusoBuildSpec{Project: project, Service: project + "-" + service, Ref: "0123456789ab"},
	})
}

func isQueued(b *kube.KusoBuild) bool {
	return b.Labels[LabelBuildState] == "queued"
}

// PERF-1: an admitted build has a CR but no pod for the seconds it takes
// the controller to render its Job. Counting only pods admitted every
// build of a monorepo push at once.
func TestAdmission_CountsStartedBuildWithoutPod(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedStartedBuild("alpha-api-started", "alpha", "api", nil),
	)
	s.MaxConcurrentBuilds = 1
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: "aabbccddeeff00112233445566778899aabbccdd"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !isQueued(got) {
		t.Error("build admitted past the cap while another build had a CR but no pod yet")
	}
}

func TestAdmission_BackToBackCreatesRespectCap(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "a"),
		seedService("alpha", "b"),
		seedService("alpha", "c"),
	)
	s.MaxConcurrentBuilds = 2
	queued := 0
	for _, svc := range []string{"a", "b", "c"} {
		got, err := s.Create(context.Background(), "alpha", svc, CreateBuildRequest{Ref: "aabbccddeeff00112233445566778899aabbccdd"})
		if err != nil {
			t.Fatalf("Create %s: %v", svc, err)
		}
		if isQueued(got) {
			queued++
		}
	}
	if queued != 1 {
		t.Errorf("queued %d of 3 builds at cap 2, want 1", queued)
	}
}

// A rebuild that waits in the queue must still push its own tag when
// dispatched; deriving it from spec.ref brought back BLD-5's shared tag.
func TestDispatchKeepsQueuedBuildsOwnTag(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedStartedBuild("alpha-api-started", "alpha", "api", nil),
	)
	s.MaxConcurrentBuilds = 1
	ctx := context.Background()
	b, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref, TriggeredBy: "user"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := b.Annotations[annImageTag]
	if !isQueued(b) || want == "" || want == ImageTag(ref) {
		t.Fatalf("queued=%v tag annotation=%q", isQueued(b), want)
	}
	if err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Delete(ctx, "alpha-api-started", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("free the slot: %v", err)
	}
	p := &Poller{Svc: s, Logger: slog.Default()}
	p.dispatchQueued(ctx, "kuso")
	got, err := s.Kube.GetKusoBuild(ctx, "kuso", b.Name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != want {
		t.Errorf("dispatched image = %+v, want tag %q", got.Spec.Image, want)
	}
}

// A build whose Job already succeeded is only promoting (maybe waiting on
// a long migration) and uses no build resources.
func TestAdmission_IgnoresBuildsPastTheirJob(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedStartedBuild("alpha-api-migrating", "alpha", "api", map[string]string{annJobSucceeded: "2026-10-07T00:00:00Z"}),
		seedStartedBuild("alpha-cms-held", "alpha", "cms", map[string]string{annPromoteHold: "waiting for sibling"}),
	)
	s.MaxConcurrentBuilds = 1
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: "aabbccddeeff00112233445566778899aabbccdd"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if isQueued(got) {
		t.Error("builds past their Job held a build slot")
	}
}
