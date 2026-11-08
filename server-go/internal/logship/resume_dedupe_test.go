package logship

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// A fresh leader rewinds resumeOverlap before the newest stored line. The
// lines it already stored must not be stored again; one the previous
// leader lost in its unflushed buffer still must be.
func TestResumeAfterLeaderChangeSkipsStoredLines(t *testing.T) {
	base := time.Date(2026, 11, 8, 18, 27, 39, 0, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	logs.add("web", "main", base.Add(100*time.Millisecond), "ERROR boom")
	logs.add("web", "main", base.Add(200*time.Millisecond), "ERROR boom")
	logs.add("web", "main", base.Add(300*time.Millisecond), "lost in buffer")
	logs.add("web", "main", base.Add(5*time.Second), "new")
	s, _ := newTestShipper(logs, testPod("web", corev1.PodRunning, false))
	// Ingest time of the newest stored line: after both ERRORs were emitted.
	s.lastStoredTs = func(context.Context, string, string, string, time.Time) (time.Time, error) {
		return base.Add(400 * time.Millisecond), nil
	}
	s.storedSince = func(context.Context, string, string, string, time.Time) ([]string, error) {
		return []string{"ERROR boom", "ERROR boom"}, nil
	}
	tick(t, s)
	assertExactlyOnce(t, shipped(s), "web: lost in buffer", "web: new")
}
