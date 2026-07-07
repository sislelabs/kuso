package builds

import (
	"testing"
	"time"
)

func ts(sec int) time.Time { return time.Unix(int64(sec), 0) }

// TestImagesToUntag covers the rollback-window decision: keep the N
// newest per service, untag the rest, scoped per (project, service).
func TestImagesToUntag(t *testing.T) {
	mk := func(name, svc, tag string, sec int) imageRetentionRecord {
		return imageRetentionRecord{buildName: name, service: svc, imageTag: tag, createdAt: ts(sec), succeeded: true}
	}

	t.Run("keeps N newest, untags older", func(t *testing.T) {
		byKey := map[svcKey][]imageRetentionRecord{
			{"proj", "web"}: {
				mk("b1", "web", "t1", 100), // newest
				mk("b2", "web", "t2", 90),
				mk("b3", "web", "t3", 80),
				mk("b4", "web", "t4", 70), // oldest → untag (keep=3)
				mk("b5", "web", "t5", 60), // → untag
			},
		}
		got := imagesToUntag(byKey, 3, nil)
		if len(got) != 2 {
			t.Fatalf("got %d untag targets, want 2: %+v", len(got), got)
		}
		// The two oldest tags must be the ones untagged.
		untagged := map[string]bool{}
		for _, g := range got {
			if g.repo != "proj/web" {
				t.Errorf("repo = %q, want proj/web", g.repo)
			}
			untagged[g.tag] = true
		}
		if !untagged["t4"] || !untagged["t5"] {
			t.Errorf("expected t4+t5 untagged, got %v", untagged)
		}
		if untagged["t1"] || untagged["t2"] || untagged["t3"] {
			t.Errorf("a kept (newest-3) tag was untagged: %v", untagged)
		}
	})

	t.Run("under window → nothing untagged", func(t *testing.T) {
		byKey := map[svcKey][]imageRetentionRecord{
			{"proj", "web"}: {mk("b1", "web", "t1", 100), mk("b2", "web", "t2", 90)},
		}
		if got := imagesToUntag(byKey, 3, nil); len(got) != 0 {
			t.Errorf("got %d, want 0 (2 builds, keep 3)", len(got))
		}
	})

	t.Run("scoped per service", func(t *testing.T) {
		byKey := map[svcKey][]imageRetentionRecord{
			{"proj", "web"}: {mk("w1", "web", "wt1", 100), mk("w2", "web", "wt2", 90), mk("w3", "web", "wt3", 80)},
			{"proj", "api"}: {mk("a1", "api", "at1", 100), mk("a2", "api", "at2", 90)},
		}
		// keep=1 → web untags 2 (wt2,wt3), api untags 1 (at2).
		got := imagesToUntag(byKey, 1, nil)
		if len(got) != 3 {
			t.Fatalf("got %d, want 3", len(got))
		}
		byRepo := map[string]int{}
		for _, g := range got {
			byRepo[g.repo]++
		}
		if byRepo["proj/web"] != 2 || byRepo["proj/api"] != 1 {
			t.Errorf("per-service untag counts wrong: %v", byRepo)
		}
	})

	// A tag a live KusoEnvironment still runs must NEVER be untagged, no
	// matter how far outside the keep-window it falls. Real incident: a
	// staging env pinned to a May build got its tag swept (production
	// kept building, staging didn't), so the next pod restart was
	// ImagePullBackOff — the env was un-restartable until a manual
	// rebuild.
	t.Run("live env tags are protected", func(t *testing.T) {
		byKey := map[svcKey][]imageRetentionRecord{
			{"proj", "web"}: {
				mk("b1", "web", "t1", 100),
				mk("b2", "web", "t2", 90),
				mk("b3", "web", "t3", 80), // outside keep=1 but LIVE on an env
				mk("b4", "web", "t4", 70), // outside keep=1 → untag
			},
		}
		protected := map[string]bool{"proj/web:t3": true}
		got := imagesToUntag(byKey, 1, protected)
		tags := map[string]bool{}
		for _, g := range got {
			tags[g.tag] = true
		}
		if tags["t3"] {
			t.Fatalf("live env tag t3 was untagged: %+v", got)
		}
		if !tags["t2"] || !tags["t4"] {
			t.Errorf("expected t2+t4 untagged, got %v", tags)
		}
	})
}
