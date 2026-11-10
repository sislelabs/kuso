package builds

import (
	"testing"
	"time"
)

// B-29: release-failed / cancelled builds that pushed an image were never
// untagged because only succeeded builds were considered.
func TestImagesToUntag_NeverPromotedImagesAgeOut(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	orig := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = orig })

	at := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	rec := func(name, tag string, h int, ok bool) imageRetentionRecord {
		return imageRetentionRecord{buildName: name, service: "web", imageTag: tag, createdAt: at(h), succeeded: ok}
	}
	byKey := map[svcKey][]imageRetentionRecord{
		{"proj", "web"}: {
			rec("ok1", "t-ok1", 1, true),
			rec("old-fail", "t-old-fail", 200, false),        // older than newest OK and past grace → untag
			rec("fresh-fail", "t-fresh-fail", 2, false),      // within grace → keep
			rec("newer-fail", "t-newer-fail", 0, false),      // newer than the newest OK → keep
			rec("shares-kept", "t-ok1", 300, false),          // same tag as a kept build → keep
			rec("protected-fail", "t-protected", 300, false), // a live env runs it → keep
		},
	}
	got := imagesToUntag(byKey, 3, map[string]bool{"proj/web:t-protected": true})
	if len(got) != 1 || got[0].tag != "t-old-fail" {
		t.Fatalf("untag targets = %+v, want only t-old-fail", got)
	}
	// Failed builds take no window slot: with keep=1 and one succeeded
	// build, nothing succeeded is untagged.
	for _, g := range imagesToUntag(byKey, 1, nil) {
		if g.tag == "t-ok1" {
			t.Errorf("succeeded build in the window was untagged")
		}
	}
}

// With no succeeded build at all nothing replaced the failed image, so it
// stays (it may be the only artifact of that commit).
func TestImagesToUntag_FailedImageKeptWithoutAnySuccess(t *testing.T) {
	now := time.Now()
	byKey := map[svcKey][]imageRetentionRecord{
		{"proj", "web"}: {{buildName: "f", service: "web", imageTag: "t", createdAt: now.Add(-500 * time.Hour)}},
	}
	if got := imagesToUntag(byKey, 3, nil); len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
}
