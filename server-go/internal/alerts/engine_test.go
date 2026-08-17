package alerts

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// Pure / no-Postgres tests. These always run.
// ---------------------------------------------------------------------------

// TestSummary locks the trim behavior that keeps alert bodies legible
// when the user pastes a 200-char regex as the rule query.
func TestSummary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"short unchanged", "connection refused", 80, "connection refused"},
		{"exactly max unchanged", strings.Repeat("a", 80), 80, strings.Repeat("a", 80)},
		{"long trimmed with ellipsis", strings.Repeat("a", 81), 80, strings.Repeat("a", 80) + "…"},
		{"empty", "", 80, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := summary(tc.in, tc.maxLen); got != tc.want {
				t.Errorf("summary(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
		})
	}
}

// TestSummaryMultibyteSplit: summary() must trim on a rune boundary. A
// byte-offset slice cuts a multi-byte rune straddling maxLen in half,
// and Postgres rejects the resulting invalid UTF-8 on the
// NotificationEvent insert (notify.Emit → bell feed), so the alert
// fires but never persists.
func TestSummaryMultibyteSplit(t *testing.T) {
	t.Parallel()
	got := summary(strings.Repeat("é", 50), 81) // "é" is 2 bytes; 81 splits one
	if !utf8.ValidString(got) {
		t.Fatalf("summary() emitted invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("é", 40) + "…"; got != want {
		t.Fatalf("summary() = %q, want %q", got, want)
	}
}

// TestEvaluateUnknownKindErrors: a rule kind with no eval logic must
// surface an error (tick logs + continues) rather than silently firing
// or silently passing.
func TestEvaluateUnknownKindErrors(t *testing.T) {
	t.Parallel()
	e := New(nil, nil, nil, nil, slogDiscard())
	fired, body, err := e.evaluate(context.Background(), &db.AlertRule{ID: "r1", Kind: "bogus"}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected error for unknown alert kind, got nil")
	}
	if fired || body != "" {
		t.Errorf("unknown kind must not fire: fired=%v body=%q", fired, body)
	}
}

// TestEvaluateLogMatchWithoutLogDBFailsSafe: a log-match rule on an
// install without log shipping (LogDB nil) is skipped — no fire, no
// error — instead of crashing the engine on every tick.
func TestEvaluateLogMatchWithoutLogDBFailsSafe(t *testing.T) {
	t.Parallel()
	e := New(nil, nil, nil, nil, slogDiscard())
	one := int64(1)
	fired, body, err := e.evaluate(context.Background(), &db.AlertRule{
		ID: "r1", Kind: db.AlertKindLogMatch, Query: "panic", ThresholdInt: &one,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("nil LogDB must not error: %v", err)
	}
	if fired || body != "" {
		t.Errorf("nil LogDB must not fire: fired=%v body=%q", fired, body)
	}
}

// TestEvaluateNodeWithoutKubeFailsSafe: node rules with no kube client
// (or a client without a clientset) evaluate to "not fired" instead of
// panicking or erroring.
func TestEvaluateNodeWithoutKubeFailsSafe(t *testing.T) {
	t.Parallel()
	for _, e := range []*Engine{
		New(nil, nil, nil, nil, slogDiscard()),
		New(nil, nil, &kube.Client{}, nil, slogDiscard()),
	} {
		fired, body, err := e.evaluate(context.Background(), &db.AlertRule{
			ID: "r1", Kind: db.AlertKindNodeCPU,
		}, time.Now().UTC())
		if err != nil {
			t.Fatalf("nil kube must not error: %v", err)
		}
		if fired || body != "" {
			t.Errorf("nil kube must not fire: fired=%v body=%q", fired, body)
		}
	}
}

// ---------------------------------------------------------------------------
// Postgres-backed tests. Skip without KUSO_TEST_PG_DSN (same convention
// as internal/db and internal/audit); CI runs them against an ephemeral
// container.
// ---------------------------------------------------------------------------

// openAlertsTestDB connects to the Postgres test DB and clears the
// tables the engine reads/writes so tests don't see each other's rows.
func openAlertsTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed alerts test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := d.DB.Exec(`TRUNCATE TABLE "AlertRule", "NodeMetric", "LogLine", "NotificationEvent" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// newTestEngine wires an Engine against the test DB with a real
// dispatcher. Emit persists NotificationEvent rows synchronously (no
// Run needed), so fires are observable as alert.fired rows.
func newTestEngine(t *testing.T, d *db.DB, k *kube.Client) *Engine {
	t.Helper()
	return New(d, d.AsLogDB(), k, notify.New(d, slogDiscard(), 16), slogDiscard())
}

func countAlertFired(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM "NotificationEvent" WHERE "type" = 'alert.fired'`).Scan(&n); err != nil {
		t.Fatalf("count alert.fired: %v", err)
	}
	return n
}

func lastFiredAt(t *testing.T, d *db.DB, id string) *time.Time {
	t.Helper()
	rules, err := d.ListAlertRules(context.Background())
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	for _, r := range rules {
		if r.ID == id {
			return r.LastFiredAt
		}
	}
	t.Fatalf("rule %q not found", id)
	return nil
}

func seedLogLines(t *testing.T, d *db.DB, project, fqService, line string, n int) {
	t.Helper()
	lines := make([]db.LogLine, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, db.LogLine{
			Ts: time.Now().UTC(), Pod: "pod-0",
			Project: project, Service: fqService, Env: "production", Line: line,
		})
	}
	if err := d.AsLogDB().InsertLogLines(context.Background(), lines); err != nil {
		t.Fatalf("insert log lines: %v", err)
	}
}

func mustCreateRule(t *testing.T, d *db.DB, r db.AlertRule) {
	t.Helper()
	if err := d.CreateAlertRule(context.Background(), r); err != nil {
		t.Fatalf("create rule %s: %v", r.ID, err)
	}
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

// TestTickFiresLogMatchAndThrottles is the core edge-trigger contract:
// a breached log-match rule fires ONCE (alert.fired event + lastFiredAt
// stamp), and the immediately-following tick — condition still present —
// does NOT re-fire because the stamp is inside throttleSeconds. It also
// locks the short→FQ service prefixing (rule says "web", LogLine rows
// carry "proj-web" from the pod label).
func TestTickFiresLogMatchAndThrottles(t *testing.T) {
	d := openAlertsTestDB(t)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-logs", Name: "err spike", Enabled: true, Kind: db.AlertKindLogMatch,
		Project: "proj", Service: "web", Query: "connection refused",
		ThresholdInt: i64(3), WindowSeconds: 300, Severity: "error", ThrottleSeconds: 3600,
	})
	seedLogLines(t, d, "proj", "proj-web", "dial tcp: connection refused", 3)

	e := newTestEngine(t, d, nil)
	e.tick(context.Background())

	if got := countAlertFired(t, d); got != 1 {
		t.Fatalf("after tick 1: alert.fired events = %d, want 1", got)
	}
	stamp := lastFiredAt(t, d, "r-logs")
	if stamp == nil {
		t.Fatal("lastFiredAt not stamped after fire")
	}

	// Condition unchanged → second tick must be throttled, not spam.
	e.tick(context.Background())
	if got := countAlertFired(t, d); got != 1 {
		t.Errorf("after tick 2 (inside throttle): alert.fired events = %d, want still 1", got)
	}
}

// TestTickRearmsAfterThrottleExpiry covers both halves of recovery:
//   - throttle expired + condition STILL breached → fires again
//   - throttle expired + condition CLEARED       → stays quiet and
//     does NOT restamp lastFiredAt (so a future breach fires promptly)
func TestTickRearmsAfterThrottleExpiry(t *testing.T) {
	d := openAlertsTestDB(t)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-logs", Name: "err spike", Enabled: true, Kind: db.AlertKindLogMatch,
		Project: "proj", Service: "web", Query: "boom",
		ThresholdInt: i64(1), WindowSeconds: 300, Severity: "warn", ThrottleSeconds: 600,
	})
	seedLogLines(t, d, "proj", "proj-web", "boom", 1)
	e := newTestEngine(t, d, nil)

	e.tick(context.Background())
	if got := countAlertFired(t, d); got != 1 {
		t.Fatalf("initial fire: events = %d, want 1", got)
	}

	// Backdate the stamp past the throttle window; condition persists.
	expired := time.Now().UTC().Add(-11 * time.Minute)
	if err := d.MarkAlertFired(context.Background(), "r-logs", expired); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	e.tick(context.Background())
	if got := countAlertFired(t, d); got != 2 {
		t.Fatalf("re-arm with condition present: events = %d, want 2", got)
	}

	// Recovery: clear the logs, expire the throttle again. No fire, and
	// the stamp must stay at the backdated value (only fires restamp).
	if _, err := d.DB.Exec(`DELETE FROM "LogLine"`); err != nil {
		t.Fatalf("clear logs: %v", err)
	}
	if err := d.MarkAlertFired(context.Background(), "r-logs", expired); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	e.tick(context.Background())
	if got := countAlertFired(t, d); got != 2 {
		t.Errorf("recovered condition must not fire: events = %d, want 2", got)
	}
	if stamp := lastFiredAt(t, d, "r-logs"); stamp == nil || !stamp.Equal(expired) {
		t.Errorf("non-firing tick must not restamp lastFiredAt: got %v, want %v", stamp, expired)
	}
}

// TestTickSkipsDisabledRule: a breached but disabled rule never fires.
func TestTickSkipsDisabledRule(t *testing.T) {
	d := openAlertsTestDB(t)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-off", Name: "muted", Enabled: false, Kind: db.AlertKindLogMatch,
		Project: "proj", Service: "web", Query: "boom",
		ThresholdInt: i64(1), WindowSeconds: 300, Severity: "error", ThrottleSeconds: 60,
	})
	seedLogLines(t, d, "proj", "proj-web", "boom", 5)

	newTestEngine(t, d, nil).tick(context.Background())
	if got := countAlertFired(t, d); got != 0 {
		t.Errorf("disabled rule fired: events = %d, want 0", got)
	}
	if stamp := lastFiredAt(t, d, "r-off"); stamp != nil {
		t.Errorf("disabled rule must not be stamped, got %v", stamp)
	}
}

// TestTickBelowThresholdStaysQuiet: matches below the threshold don't
// fire and don't consume the throttle.
func TestTickBelowThresholdStaysQuiet(t *testing.T) {
	d := openAlertsTestDB(t)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-quiet", Name: "quiet", Enabled: true, Kind: db.AlertKindLogMatch,
		Project: "proj", Service: "web", Query: "boom",
		ThresholdInt: i64(5), WindowSeconds: 300, Severity: "error", ThrottleSeconds: 60,
	})
	seedLogLines(t, d, "proj", "proj-web", "boom", 4) // 4 < 5

	newTestEngine(t, d, nil).tick(context.Background())
	if got := countAlertFired(t, d); got != 0 {
		t.Errorf("below-threshold rule fired: events = %d, want 0", got)
	}
	if stamp := lastFiredAt(t, d, "r-quiet"); stamp != nil {
		t.Errorf("below-threshold rule must not be stamped, got %v", stamp)
	}
}

// TestTickContinuesPastBrokenRule: an unevaluable rule (unknown kind —
// e.g. a row written by a newer server version) logs + continues; the
// healthy rule after it still fires. Rules are listed name-ASC, so
// "aaa …" is evaluated first.
func TestTickContinuesPastBrokenRule(t *testing.T) {
	d := openAlertsTestDB(t)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-broken", Name: "aaa broken", Enabled: true, Kind: "from_the_future",
		Severity: "error", WindowSeconds: 300, ThrottleSeconds: 60,
	})
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-good", Name: "zzz good", Enabled: true, Kind: db.AlertKindLogMatch,
		Project: "proj", Service: "web", Query: "boom",
		ThresholdInt: i64(1), WindowSeconds: 300, Severity: "error", ThrottleSeconds: 60,
	})
	seedLogLines(t, d, "proj", "proj-web", "boom", 1)

	newTestEngine(t, d, nil).tick(context.Background())
	if got := countAlertFired(t, d); got != 1 {
		t.Errorf("healthy rule after broken one: events = %d, want 1", got)
	}
	if stamp := lastFiredAt(t, d, "r-broken"); stamp != nil {
		t.Errorf("broken rule must not be stamped, got %v", stamp)
	}
	if stamp := lastFiredAt(t, d, "r-good"); stamp == nil {
		t.Error("healthy rule should be stamped")
	}
}

// TestEvaluateNodeThresholds drives the node_cpu/node_mem/node_disk
// paths through evaluate() with a fake clientset (node names) + real
// NodeMetric rows: threshold crossing, staying under, the zero-capacity
// guard (no divide-by-zero fire on a node metrics-server can't read),
// and latest-sample-wins.
func TestEvaluateNodeThresholds(t *testing.T) {
	d := openAlertsTestDB(t)
	ctx := context.Background()
	cs := kubefake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
	)
	e := newTestEngine(t, d, &kube.Client{Clientset: cs})
	now := time.Now().UTC()

	insert := func(m db.NodeMetric) {
		t.Helper()
		if err := d.InsertNodeMetric(ctx, m); err != nil {
			t.Fatalf("insert metric: %v", err)
		}
	}
	reset := func() {
		t.Helper()
		if _, err := d.DB.Exec(`DELETE FROM "NodeMetric"`); err != nil {
			t.Fatalf("reset metrics: %v", err)
		}
	}
	evalRule := func(kind string, threshold float64) (bool, string) {
		t.Helper()
		fired, body, err := e.evaluate(ctx, &db.AlertRule{
			ID: "r-node", Kind: kind, ThresholdFloat: f64(threshold), WindowSeconds: 300,
		}, now)
		if err != nil {
			t.Fatalf("evaluate %s: %v", kind, err)
		}
		return fired, body
	}

	t.Run("cpu over threshold fires with node in body", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now, CPUUsedMilli: 900, CPUCapacityMilli: 1000})
		fired, body := evalRule(db.AlertKindNodeCPU, 80)
		if !fired {
			t.Fatal("90% CPU vs 80% threshold should fire")
		}
		if !strings.Contains(body, "n1=90.0%") {
			t.Errorf("body should name the hot node, got %q", body)
		}
	})

	t.Run("cpu under threshold stays quiet", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now, CPUUsedMilli: 700, CPUCapacityMilli: 1000})
		if fired, body := evalRule(db.AlertKindNodeCPU, 80); fired {
			t.Errorf("70%% CPU vs 80%% threshold fired: %q", body)
		}
	})

	t.Run("zero capacity fails safe", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now, CPUUsedMilli: 900, CPUCapacityMilli: 0})
		if fired, body := evalRule(db.AlertKindNodeCPU, 80); fired {
			t.Errorf("zero capacity must not fire (unreadable input), got %q", body)
		}
	})

	t.Run("latest sample wins", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now.Add(-10 * time.Minute), CPUUsedMilli: 990, CPUCapacityMilli: 1000})
		insert(db.NodeMetric{Node: "n1", Ts: now, CPUUsedMilli: 100, CPUCapacityMilli: 1000})
		if fired, body := evalRule(db.AlertKindNodeCPU, 80); fired {
			t.Errorf("older hot sample must not fire once the latest is cool, got %q", body)
		}
	})

	t.Run("mem over threshold fires", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now, MemUsedBytes: 95, MemCapacityBytes: 100})
		fired, body := evalRule(db.AlertKindNodeMem, 90)
		if !fired || !strings.Contains(body, "MEM") {
			t.Errorf("95%% mem vs 90%% should fire with MEM body, fired=%v body=%q", fired, body)
		}
	})

	t.Run("disk uses capacity minus avail", func(t *testing.T) {
		reset()
		insert(db.NodeMetric{Node: "n1", Ts: now, DiskAvailBytes: 10, DiskCapacityBytes: 100})
		fired, body := evalRule(db.AlertKindNodeDisk, 85)
		if !fired || !strings.Contains(body, "n1=90.0%") {
			t.Errorf("90%% disk used vs 85%% should fire, fired=%v body=%q", fired, body)
		}
	})

	t.Run("no samples stays quiet", func(t *testing.T) {
		reset()
		if fired, body := evalRule(db.AlertKindNodeCPU, 80); fired {
			t.Errorf("no metric rows must not fire, got %q", body)
		}
	})
}
