package handlers

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

// Tests for the agent-use W4 truncation contract: capped list
// responses must be distinguishable from complete ones. The bare-array
// endpoints signal via X-Kuso-Truncated / X-Kuso-Next-* headers; the
// windowing itself is a pure function tested here without handler
// wiring.

func summaries(n int) []buildSummary {
	out := make([]buildSummary, n)
	for i := range out {
		out[i] = buildSummary{ID: fmt.Sprintf("b%d", i), Status: "succeeded"}
	}
	return out
}

func TestPaginateBuildSummaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		total         int
		limit, offset int
		wantLen       int
		wantFirst     string
		wantTrunc     bool
		wantNext      int
	}{
		{"no params = legacy full response", 5, 0, 0, 5, "b0", false, 0},
		{"limit cuts and signals", 5, 2, 0, 2, "b0", true, 2},
		{"offset+limit middle page", 5, 2, 2, 2, "b2", true, 4},
		{"final partial page is complete", 5, 2, 4, 1, "b4", false, 0},
		{"limit == remaining is complete (no false positive)", 5, 5, 0, 5, "b0", false, 0},
		{"offset past end = empty, complete", 5, 2, 10, 0, "", false, 0},
		{"offset alone tails the list", 5, 0, 3, 2, "b3", false, 0},
		{"negative offset treated as zero", 5, 2, -3, 2, "b0", true, 2},
		{"empty input", 0, 2, 0, 0, "", false, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page, trunc, next := paginateBuildSummaries(summaries(tc.total), tc.limit, tc.offset)
			if len(page) != tc.wantLen {
				t.Fatalf("len=%d, want %d", len(page), tc.wantLen)
			}
			if tc.wantLen > 0 && page[0].ID != tc.wantFirst {
				t.Errorf("first=%s, want %s", page[0].ID, tc.wantFirst)
			}
			if trunc != tc.wantTrunc {
				t.Errorf("truncated=%v, want %v", trunc, tc.wantTrunc)
			}
			if trunc && next != tc.wantNext {
				t.Errorf("next=%d, want %d", next, tc.wantNext)
			}
		})
	}
}

func TestSetTruncationHeaders(t *testing.T) {
	t.Parallel()
	t.Run("offset cursor", func(t *testing.T) {
		w := httptest.NewRecorder()
		setTruncationHeaders(w, headerNextOffset, "40")
		if got := w.Header().Get(headerTruncated); got != "true" {
			t.Errorf("%s = %q, want true", headerTruncated, got)
		}
		if got := w.Header().Get(headerNextOffset); got != "40" {
			t.Errorf("%s = %q, want 40", headerNextOffset, got)
		}
		if got := w.Header().Get(headerNextAfter); got != "" {
			t.Errorf("%s unexpectedly set: %q", headerNextAfter, got)
		}
	})
	t.Run("keyset cursor", func(t *testing.T) {
		w := httptest.NewRecorder()
		setTruncationHeaders(w, headerNextAfter, "1234")
		if got := w.Header().Get(headerNextAfter); got != "1234" {
			t.Errorf("%s = %q, want 1234", headerNextAfter, got)
		}
	})
	t.Run("empty cursor only marks truncation", func(t *testing.T) {
		w := httptest.NewRecorder()
		setTruncationHeaders(w, headerNextOffset, "")
		if got := w.Header().Get(headerTruncated); got != "true" {
			t.Errorf("%s = %q, want true", headerTruncated, got)
		}
		if got := w.Header().Get(headerNextOffset); got != "" {
			t.Errorf("cursor should be absent, got %q", got)
		}
	})
}
