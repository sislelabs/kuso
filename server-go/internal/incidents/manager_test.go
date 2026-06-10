package incidents

import (
	"testing"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

func TestDecide(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	const cooldown = time.Hour
	cases := []struct {
		name       string
		openExists bool
		lastClosed time.Time
		lastOK     bool
		openCount  int
		maxConc    int
		cooldown   time.Duration
		want       spawnDecision
	}{
		{"no open, never closed, room → spawn", false, time.Time{}, false, 0, 3, cooldown, decideSpawn},
		{"open exists → attach", true, time.Time{}, false, 1, 3, cooldown, decideAttach},
		{"open exists wins over cap", true, time.Time{}, false, 9, 3, cooldown, decideAttach},
		{"closed recently → cooldown drop", false, now.Add(-30 * time.Minute), true, 0, 3, cooldown, decideDrop},
		{"closed long ago → spawn", false, now.Add(-2 * time.Hour), true, 0, 3, cooldown, decideSpawn},
		{"at cap → drop", false, time.Time{}, false, 3, 3, cooldown, decideDrop},
		{"over cap → drop", false, time.Time{}, false, 5, 3, cooldown, decideDrop},
		{"cooldown takes priority over cap room", false, now.Add(-1 * time.Minute), true, 0, 3, cooldown, decideDrop},
		// Cooldown of 0 disables the cooldown gate entirely.
		{"zero cooldown → recently-closed still spawns", false, now.Add(-1 * time.Minute), true, 0, 3, 0, decideSpawn},
		// MaxConcurrent of 0 disables the cap.
		{"zero cap → never cap-drops", false, time.Time{}, false, 99, 0, cooldown, decideSpawn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decide(c.openExists, c.lastClosed, c.lastOK, c.openCount, c.maxConc, c.cooldown, now)
			if got != c.want {
				t.Errorf("decide = %v, want %v", got, c.want)
			}
		})
	}
}

// TestTriggerEnabled / Hook gating: a disabled config or a disabled trigger
// type is a no-op.
func TestTriggerEnabled(t *testing.T) {
	t.Parallel()
	cfg := db.IncidentAgentConfig{TriggerPod: true, TriggerAlert: false, TriggerNode: true}
	if !triggerEnabled(cfg, notify.EventPodCrashed) {
		t.Error("pod should be enabled")
	}
	if triggerEnabled(cfg, notify.EventAlertFired) {
		t.Error("alert should be disabled")
	}
	if !triggerEnabled(cfg, notify.EventNodeUnreachable) {
		t.Error("node should be enabled")
	}
	if triggerEnabled(cfg, notify.EventBuildFailed) {
		t.Error("non-trigger event must be false")
	}
}

func TestTargetKeyFor(t *testing.T) {
	t.Parallel()
	// pod.crashed keys on project|service.
	pod := notify.Event{Type: notify.EventPodCrashed, Project: "berivangold", Service: "web"}
	if got := targetKeyFor(pod); got != "pod.crashed|berivangold|web" {
		t.Errorf("pod key = %q", got)
	}
	// node.unreachable keys on the node name from a field, not Service.
	node := notify.Event{
		Type:   notify.EventNodeUnreachable,
		Fields: []notify.EventField{{Name: "Node", Value: "server2"}},
	}
	if got := targetKeyFor(node); got != "node.unreachable||server2" {
		t.Errorf("node key = %q", got)
	}
	// Two crashes of the same pod/service produce the SAME key (dedup).
	a := notify.Event{Type: notify.EventPodCrashed, Project: "p", Service: "s"}
	b := notify.Event{Type: notify.EventPodCrashed, Project: "p", Service: "s", Title: "different title"}
	if targetKeyFor(a) != targetKeyFor(b) {
		t.Error("same pod+service must dedup regardless of title")
	}
}

func TestContextPackRoundTrips(t *testing.T) {
	t.Parallel()
	e := notify.Event{
		Type: notify.EventPodCrashed, Title: "crash", Project: "p", Service: "s",
		LogTail: "panic: nil", Fields: []notify.EventField{{Name: "Restarts", Value: "4"}},
	}
	pack := contextPack(e)
	if len(pack) == 0 || string(pack) == "{}" {
		t.Fatalf("pack empty: %s", pack)
	}
	// Must contain the fields map + log tail.
	s := string(pack)
	for _, want := range []string{`"type":"pod.crashed"`, `"logTail":"panic: nil"`, `"Restarts":"4"`} {
		if !contains(s, want) {
			t.Errorf("pack missing %q: %s", want, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
