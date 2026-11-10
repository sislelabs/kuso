package builds

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDoneBuildNeedsPoll(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-3 * time.Hour)
	mk := func(created time.Time, ann map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetCreationTimestamp(metav1.NewTime(created))
		u.SetAnnotations(ann)
		return u
	}
	cases := []struct {
		name string
		u    *unstructured.Unstructured
		want bool
	}{
		{"old, final status posted", mk(old, map[string]string{annCommitStatus: "success"}), false},
		{"old, never posted (past backfill)", mk(old, nil), false},
		{"old, release retry requested", mk(old, map[string]string{annRetryRelease: "x"}), true},
		{"old, non-final status posted", mk(old, map[string]string{annCommitStatus: "pending:running"}), true},
		{"created long ago, completed recently", mk(old, map[string]string{
			annCommitStatus: "success", annCompletedAt: now.Add(-10 * time.Minute).Format(time.RFC3339)}), true},
		{"recent", mk(now.Add(-time.Minute), map[string]string{annCommitStatus: "success"}), true},
	}
	for _, tc := range cases {
		if got := doneBuildNeedsPoll(tc.u, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
