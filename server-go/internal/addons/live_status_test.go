package addons

import (
	"strings"
	"testing"

	"kuso/server/internal/podreason"
)

func TestDeriveAddonState(t *testing.T) {
	unsched := &podreason.Issue{Kind: podreason.KindUnschedulable, Summary: "The node is low on disk, so the pod can't be scheduled"}
	cases := []struct {
		name   string
		in     addonStateInput
		state  string
		detail string
	}{
		// The reported bug: helm Deployed=True but the pod can't schedule.
		{"deployed but unschedulable", addonStateInput{Deployed: true, Total: 1, Young: true, Issue: unsched}, addonStateStuck, "low on disk"},
		{"all ready", addonStateInput{Deployed: true, Ready: 1, Total: 1}, addonStateLive, "1/1"},
		{"helm failed wins", addonStateInput{Failed: true, FailMessage: "boom", Ready: 1, Total: 1}, addonStateFailing, "boom"},
		{"deployed, no pods", addonStateInput{Deployed: true}, addonStateFailing, "No pods"},
		{"still starting", addonStateInput{Total: 1, Young: true}, addonStateProvisioning, "starting"},
		{"long not ready", addonStateInput{Deployed: true, Total: 1}, addonStateFailing, "0/1"},
		{"partly ready", addonStateInput{Deployed: true, Ready: 1, Total: 2}, addonStateDegraded, "1/2"},
		{"external", addonStateInput{External: true}, addonStateExternal, ""},
	}
	for _, c := range cases {
		state, detail := deriveAddonState(c.in)
		if state != c.state || !strings.Contains(detail, c.detail) {
			t.Errorf("%s: got (%q, %q), want (%q, ~%q)", c.name, state, detail, c.state, c.detail)
		}
	}
}

func TestHelmConditions(t *testing.T) {
	failed, msg, deployed := helmConditions(map[string]any{"conditions": []any{
		map[string]any{"type": "Deployed", "status": "True"},
		map[string]any{"type": "ReleaseFailed", "status": "True", "message": "upgrade failed"},
	}})
	if !failed || msg != "upgrade failed" || !deployed {
		t.Fatalf("got failed=%v msg=%q deployed=%v", failed, msg, deployed)
	}
}
