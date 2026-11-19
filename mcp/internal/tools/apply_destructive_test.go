package tools

import (
	"net/http"
	"strings"
	"testing"
)

const destructivePlan = `{"servicesToUpdate":["web"],"changes":[{"resource":"service:web","fields":[{"field":"domains","from":"a.com","to":"","destructive":true}]}],"warnings":["key X missing"],"execChanges":[{"resource":"service:web","fields":["command"]}]}`

// plan used to decode only the *To* arrays, so a dropped domain showed
// as "~ Services web" and apply ran it without confirm.
func TestPlanRendersDestructiveChanges(t *testing.T) {
	sess, _ := newRecordingSession(t, func(r *http.Request) (int, string) {
		return http.StatusOK, destructivePlan
	})
	text, isErr, err := callText(t, sess, "plan", map[string]any{"project": "shop", "yaml": "project: shop\n"})
	if err != nil || isErr {
		t.Fatalf("plan failed: err=%v text=%s", err, text)
	}
	for _, want := range []string{"[DESTRUCTIVE]", "domains: a.com → ", "key X missing", "command", "confirm=true"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan text missing %q:\n%s", want, text)
		}
	}
}

func TestApplyDestructiveNeedsConfirm(t *testing.T) {
	sess, reqs := newRecordingSession(t, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("dryRun") == "1" {
			return http.StatusOK, destructivePlan
		}
		return http.StatusOK, `{"plan":` + destructivePlan + `}`
	})
	text, isErr, _ := callText(t, sess, "apply", map[string]any{"project": "shop", "yaml": "project: shop\n"})
	if !isErr || !strings.Contains(text, "confirm=true") || !strings.Contains(text, "service:web.domains") {
		t.Fatalf("want a confirm refusal naming the change, got isErr=%v text=%s", isErr, text)
	}
	if got := reqs(); len(got) != 1 {
		t.Fatalf("only the dry-run plan may be sent, got %+v", got)
	}
	text, isErr, err := callText(t, sess, "apply", map[string]any{"project": "shop", "yaml": "project: shop\n", "confirm": true})
	if err != nil || isErr {
		t.Fatalf("confirmed apply failed: err=%v text=%s", err, text)
	}
}
