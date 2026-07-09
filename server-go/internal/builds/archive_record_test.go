package builds

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type captureArchiver struct {
	rec BuildArchiveRecord
}

func (c *captureArchiver) SaveBuildLog(context.Context, string, string, string, string, string) error {
	return nil
}
func (c *captureArchiver) SaveBuildRecord(_ context.Context, r BuildArchiveRecord) error {
	c.rec = r
	return nil
}

// TestArchiveRecord_FinishedAtNeverEmpty pins the archive snapshot's
// FinishedAt contract. The succeeded/failed transitions patch the
// build-completed-at annotation onto the CR via the API and then
// archive the STALE in-memory object, so every archived record carried
// FinishedAt:"" — the canvas/deployments backfill then had no finish
// time for builds whose CR the retention sweep deleted. At a terminal
// phase the archive must stamp a fallback when the annotation is
// missing, and pass the annotation through verbatim when present.
func TestArchiveRecord_FinishedAtNeverEmpty(t *testing.T) {
	t.Parallel()

	build := func(ann map[string]string) *kube.KusoBuild {
		return &kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "p-web-abc", Annotations: ann},
			Spec:       kube.KusoBuildSpec{Project: "p", Service: "p-web", Branch: "main"},
		}
	}

	t.Run("missing annotation → fallback stamp", func(t *testing.T) {
		cap := &captureArchiver{}
		p := &Poller{LogArchive: cap}
		p.archiveRecord(context.Background(), build(nil), "succeeded")
		if cap.rec.FinishedAt == "" {
			t.Fatalf("archived succeeded build has empty FinishedAt: %+v", cap.rec)
		}
	})

	t.Run("annotation present → passed through verbatim", func(t *testing.T) {
		cap := &captureArchiver{}
		p := &Poller{LogArchive: cap}
		p.archiveRecord(context.Background(), build(map[string]string{
			annCompletedAt: "2026-07-08T11:41:00Z",
		}), "succeeded")
		if cap.rec.FinishedAt != "2026-07-08T11:41:00Z" {
			t.Fatalf("FinishedAt = %q, want the annotation value", cap.rec.FinishedAt)
		}
	})
}
