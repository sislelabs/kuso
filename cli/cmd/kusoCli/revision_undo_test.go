package kusoCli

import (
	"encoding/json"
	"strings"
	"testing"
)

func rev(id, at, snapshot string) revisionRow {
	return revisionRow{ID: id, CreatedAt: at, Snapshot: json.RawMessage(snapshot)}
}

func TestPlanUndoReplaysSameFieldRevision(t *testing.T) {
	revs := []revisionRow{
		rev("old-port", "2026-11-09T09:30:00Z", `{"patch":{"port":3000}}`),
		rev("old-hc", "2026-11-09T09:00:00Z", `{"patch":{"healthcheck":{"path":"/"}}}`),
		rev("secret", "2026-11-09T10:30:00Z", `{"op":"env.secret","informational":true,"keys":["A"]}`),
		rev("bad-hc", "2026-11-09T10:00:00Z", `{"patch":{"healthcheck":{"path":"/nope"}}}`),
	}
	plan, err := planUndo(revs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.change.ID != "bad-hc" || plan.target == nil || plan.target.ID != "old-hc" {
		t.Fatalf("change=%s target=%v; want bad-hc → old-hc", plan.change.ID, plan.target)
	}
}

func TestPlanUndoFallsBackToDefault(t *testing.T) {
	revs := []revisionRow{
		rev("first-hc", "2026-11-09T10:00:00Z", `{"patch":{"healthcheck":{"path":"/nope"}}}`),
		rev("port", "2026-11-09T09:00:00Z", `{"patch":{"port":3000}}`),
	}
	plan, err := planUndo(revs)
	if err != nil {
		t.Fatal(err)
	}
	if plan.target != nil || plan.defaultPatch == nil || plan.defaultPatch.Healthcheck == nil || plan.defaultPatch.Healthcheck.Path != "" {
		t.Fatalf("want a default healthcheck patch, got %+v", plan)
	}
	body, _ := json.Marshal(plan.defaultPatch)
	if string(body) != `{"healthcheck":{"path":""}}` {
		t.Errorf("patch body %s", body)
	}
}

func TestPlanUndoRefusesFieldWithoutDefault(t *testing.T) {
	_, err := planUndo([]revisionRow{rev("p", "2026-11-09T10:00:00Z", `{"patch":{"port":3000}}`)})
	if err == nil || !strings.Contains(err.Error(), "no default") {
		t.Fatalf("got %v", err)
	}
}

func TestRevisionChangedText(t *testing.T) {
	r := rev("x", "2026-11-09T10:00:00Z", `{"patch":{"healthcheck":{},"port":1},"revertOf":"5144e1f1-591a-4fd4"}`)
	if got := revisionChangedText(r); got != "healthcheck, port (revert of 5144e1f1)" {
		t.Errorf("got %q", got)
	}
	s := revisionRow{Snapshot: json.RawMessage(`{"op":"env.secret","informational":true}`), ChangedKeys: []string{"A", "B"}}
	if got := revisionChangedText(s); got != "env A, B" {
		t.Errorf("secret write: got %q", got)
	}
}
