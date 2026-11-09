package kusoCli

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestServiceStatusLabelMatchesWebVocabulary(t *testing.T) {
	cases := map[string]string{
		"running":         "Live",
		"deploying":       "Deploying",
		"building":        "Building",
		"degraded":        "Degraded",
		"release_failed":  "Degraded",
		"crashlooping":    "Failing",
		"build_failed":    "Failing",
		"stuck":           "Stuck",
		"rollout_failing": "Rollout failing",
		"sleeping":        "Sleeping",
		"stopped":         "Stopped",
		"no_image":        "Not deployed",
		"unknown":         "Unknown",
		"from_the_future": "from_the_future",
		"":                "",
	}
	for state, want := range cases {
		if got := serviceStatusLabel(state); got != want {
			t.Errorf("serviceStatusLabel(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestEnvStatusReason(t *testing.T) {
	cases := []struct {
		status map[string]any
		want   string
	}{
		{map[string]any{"state": "running", "stateDetail": "1/1 replicas ready"}, ""},
		{map[string]any{"state": "build_failed", "stateDetail": "nothing is running"}, "Latest build failed: nothing is running"},
		{map[string]any{"state": "stuck", "stateDetail": "image can't be pulled"}, "Can't start: image can't be pulled"},
		{map[string]any{"state": "rollout_failing", "stateDetail": "the new version fails its health check (GET /h → 404); the previous version is still serving"},
			"The new version fails its health check (GET /h → 404); the previous version is still serving"},
		{map[string]any{"state": "deploying", "stateDetail": "rolling out — 0/1 replicas updated"}, "Rolling out — 0/1 replicas updated"},
	}
	for _, c := range cases {
		if got := envStatusReason(c.status); got != c.want {
			t.Errorf("envStatusReason(%v) = %q, want %q", c.status["state"], got, c.want)
		}
	}
}

func TestEnvStatusFactsRollout(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	status := map[string]any{
		"state": "rollout_failing",
		"rollout": map[string]any{
			"failing": true, "previousServing": true, "kind": "healthcheck",
			"healthcheckPath": "/health",
			"since":           "2026-11-09T11:55:00Z",
			"previousSince":   "2026-11-09T09:00:00Z",
		},
	}
	want := []string{
		"new version failing for 5m; previous version serving (live 3h)",
		"health check GET /health",
	}
	if got := envStatusFacts(status, now); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// With a probe result the cause already names the path and code.
	status["rollout"].(map[string]any)["probeResult"] = "404"
	if got := envStatusFacts(status, now); !reflect.DeepEqual(got, want[:1]) {
		t.Errorf("with probe: got %q", got)
	}
}

func TestEnvStatusFactsStuck(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	status := map[string]any{
		"state": "stuck",
		"stuck": map[string]any{
			"reason": "unschedulable", "summary": "no node has room",
			"message": "0/3 nodes are available:\n  3 Insufficient memory.",
			"since":   "2026-11-09T11:48:00Z",
		},
	}
	want := []string{"reason unschedulable, for 12m", "0/3 nodes are available: 3 Insufficient memory."}
	if got := envStatusFacts(status, now); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEnvStatusShortReasonKeepsPreviousServing(t *testing.T) {
	status := map[string]any{
		"state":       "rollout_failing",
		"stateDetail": strings.Repeat("x", 200),
		"rollout": map[string]any{
			"previousServing": true,
			"cause":           "the new version fails its health check (GET /health → 404)",
		},
	}
	got := envStatusShortReason(status)
	if !strings.HasPrefix(got, "Previous version serving; the new version fails") {
		t.Errorf("got %q", got)
	}
}

func TestErrorSummaryLine(t *testing.T) {
	body := []byte(`{"since":"2026-11-08T00:00:00Z","services":{"web":{"occurrences":2,"groups":1},"api":{"occurrences":9,"groups":3}}}`)
	want := "\nerrors (24h): api 9 in 3 group(s), web 2 in 1 group(s)  (kuso service errors shop <service>)\n"
	if got := errorSummaryLine("shop", body); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := errorSummaryLine("shop", []byte(`{"services":{}}`)); got != "\nerrors (24h): none\n" {
		t.Errorf("empty: got %q", got)
	}
}
