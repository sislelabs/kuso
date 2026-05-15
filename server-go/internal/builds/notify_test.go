package builds

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestBuildRichCard_Succeeded covers the happy-path build card: title
// in "<glyph> <verb> · <project>/<service>" shape, description from
// the commit message (first line only), three inline fields in order
// Ref / By / Built in.
func TestBuildRichCard_Succeeded(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "distill-web-abc123",
			Annotations: map[string]string{
				annCommitMessage: "feat(brand): real Papelito mark\n\nlonger body\nignored",
				annTriggerUser:   "ivo9999",
				annStartedAt:     "2026-05-16T12:00:00Z",
				annCompletedAt:   "2026-05-16T12:01:24Z",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "53d3f34262ef",
		},
	}
	title, desc, fields := buildRichCard(b, "web", "succeeded", "")
	if title != "✓ Build succeeded · distill / web" {
		t.Errorf("title: %q", title)
	}
	if desc != "feat(brand): real Papelito mark" {
		t.Errorf("description should be first line only: %q", desc)
	}
	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %d (%+v)", len(fields), fields)
	}
	if fields[0].Name != "Ref" || fields[0].Value != "`main` · `53d3f34`" {
		t.Errorf("ref field: %+v", fields[0])
	}
	if fields[1].Name != "By" || fields[1].Value != "ivo9999" {
		t.Errorf("by field: %+v", fields[1])
	}
	if fields[2].Name != "Built in" || fields[2].Value != "1m 24s" {
		t.Errorf("duration field: %+v", fields[2])
	}
}

// TestBuildRichCard_Failed verifies failed-build cards use the failure
// reason as the description when no commit message is available, and
// flip the duration label.
func TestBuildRichCard_Failed(t *testing.T) {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annStartedAt:   "2026-05-16T12:00:00Z",
				annCompletedAt: "2026-05-16T12:00:42Z",
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "distill",
			Service: "distill-web",
			Branch:  "main",
			Ref:     "abc1234",
		},
	}
	title, desc, fields := buildRichCard(b, "web", "failed", "kaniko: COPY failed: not found")
	if title != "✗ Build failed · distill / web" {
		t.Errorf("title: %q", title)
	}
	if desc != "kaniko: COPY failed: not found" {
		t.Errorf("description should fall back to failure reason: %q", desc)
	}
	// No annTriggerUser → no "By" field. So we expect 2 fields.
	if len(fields) != 2 {
		t.Fatalf("expected 2 fields, got %d (%+v)", len(fields), fields)
	}
	if fields[1].Name != "Failed after" || fields[1].Value != "42s" {
		t.Errorf("duration label/value: %+v", fields[1])
	}
}

// TestBuildRichCard_NoData covers an emit where the CR has no commit
// message and no start time — the card still renders with at minimum
// the title and (if present) ref.
func TestBuildRichCard_NoData(t *testing.T) {
	b := &kube.KusoBuild{
		Spec: kube.KusoBuildSpec{
			Project: "p",
			Service: "p-s",
		},
	}
	title, desc, fields := buildRichCard(b, "s", "succeeded", "")
	if title != "✓ Build succeeded · p / s" {
		t.Errorf("title: %q", title)
	}
	if desc != "" {
		t.Errorf("description should be empty: %q", desc)
	}
	if len(fields) != 0 {
		t.Errorf("expected no fields, got %+v", fields)
	}
}

// TestFormatBuildDuration pins the human-readable duration format.
// The values are what users see in the Discord card; changing them
// is a UI change, not a refactor.
func TestFormatBuildDuration(t *testing.T) {
	tests := []struct {
		ms   int64
		want string
	}{
		{1_000, "1s"},
		{59_000, "59s"},
		{60_000, "1m"},
		{84_000, "1m 24s"},
		{3_600_000, "1h"},
		{3_900_000, "1h 5m"},
		{7_200_000, "2h"},
	}
	for _, tc := range tests {
		if got := formatBuildDuration(tc.ms); got != tc.want {
			t.Errorf("formatBuildDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

// TestBuildDurationMs handles the failure modes: missing stamps,
// malformed times, end-before-start. All return 0 so the field drops
// out of the card.
func TestBuildDurationMs(t *testing.T) {
	tests := []struct {
		name  string
		annos map[string]string
		want  int64
	}{
		{"both present", map[string]string{
			annStartedAt:   "2026-05-16T12:00:00Z",
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 42_000},
		{"missing start", map[string]string{
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 0},
		{"missing end", map[string]string{
			annStartedAt: "2026-05-16T12:00:00Z",
		}, 0},
		{"malformed", map[string]string{
			annStartedAt:   "not-a-time",
			annCompletedAt: "2026-05-16T12:00:42Z",
		}, 0},
		{"end before start (clock skew)", map[string]string{
			annStartedAt:   "2026-05-16T12:00:42Z",
			annCompletedAt: "2026-05-16T12:00:00Z",
		}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &kube.KusoBuild{ObjectMeta: metav1.ObjectMeta{Annotations: tc.annos}}
			if got := buildDurationMs(b); got != tc.want {
				t.Errorf("buildDurationMs() = %d, want %d", got, tc.want)
			}
		})
	}
}
