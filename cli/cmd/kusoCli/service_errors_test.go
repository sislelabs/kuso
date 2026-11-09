package kusoCli

import (
	"testing"
	"time"
)

func TestErrorGroupState(t *testing.T) {
	cases := []struct{ lastSeen, resolvedAt, want string }{
		{"2026-11-09T10:00:00.5Z", "", "open"},
		{"2026-11-09T10:00:00Z", "2026-11-09T11:00:00Z", "resolved"},
		{"2026-11-09T12:00:00.123Z", "2026-11-09T11:00:00Z", "recurred"},
	}
	for _, c := range cases {
		if got := errorGroupState(c.lastSeen, c.resolvedAt); got != c.want {
			t.Errorf("errorGroupState(%q, %q) = %q, want %q", c.lastSeen, c.resolvedAt, got, c.want)
		}
	}
}

func TestSinceDeployWindow(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-94*time.Minute - time.Second), "95m"},
		{now.Add(-10 * time.Second), "1m"},
		{now.Add(time.Minute), "1m"},
		{now.Add(-60 * 24 * time.Hour), "43200m"},
	}
	for _, c := range cases {
		if got := sinceDeployWindow(c.at, now); got != c.want {
			t.Errorf("sinceDeployWindow(%v) = %q, want %q", now.Sub(c.at), got, c.want)
		}
	}
}

func env(state string, status map[string]any, promotedAt string) map[string]any {
	status["state"] = state
	e := map[string]any{"status": status, "metadata": map[string]any{}}
	if promotedAt != "" {
		e["metadata"] = map[string]any{"annotations": map[string]any{promotedAtAnnotation: promotedAt}}
	}
	return e
}

func TestEnvDeployedAt(t *testing.T) {
	ts := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339, s)
		return t
	}
	cases := []struct {
		name string
		env  map[string]any
		want string
	}{
		{"newest stamp wins", env("running", map[string]any{
			"lastDeployedAt": "2026-11-09T08:00:00Z", "rolloutCompletedAt": "2026-11-09T09:00:00Z",
		}, "2026-11-09T08:58:00Z"), "2026-11-09T09:00:00Z"},
		{"finished promotion counts", env("running", map[string]any{
			"rolloutCompletedAt": "2026-11-09T09:00:00Z",
		}, "2026-11-09T08:59:00Z"), "2026-11-09T09:00:00Z"},
		{"unfinished promotion of a failing rollout is ignored", env("rollout_failing", map[string]any{
			"lastDeployedAt": "2026-11-09T07:00:00Z",
		}, "2026-11-09T11:00:00Z"), "2026-11-09T07:00:00Z"},
		{"failing rollout with no stamps falls back to the serving version", env("rollout_failing", map[string]any{
			"rollout": map[string]any{"previousSince": "2026-11-09T06:00:00Z"},
		}, ""), "2026-11-09T06:00:00Z"},
	}
	for _, c := range cases {
		got, ok := envDeployedAt(c.env)
		if !ok || !got.Equal(ts(c.want)) {
			t.Errorf("%s: got %v (%v), want %s", c.name, got, ok, c.want)
		}
	}
	if _, ok := envDeployedAt(env("no_image", map[string]any{}, "")); ok {
		t.Error("an env that never deployed has no deploy time")
	}
}
