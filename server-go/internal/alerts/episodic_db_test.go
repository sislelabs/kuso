package alerts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// mutableProm serves the 5xx queries from values the test can change
// between ticks.
type mutableProm struct {
	mu         sync.Mutex
	total, bad string
}

func (m *mutableProm) set(total, bad string) {
	m.mu.Lock()
	m.total, m.bad = total, bad
	m.mu.Unlock()
}

func (m *mutableProm) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	v := m.total
	if strings.Contains(r.URL.Query().Get("query"), `code=~"5.."`) {
		v = m.bad
	}
	m.mu.Unlock()
	_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"service":"` + webProd + `"},"value":[1790000000,"` + v + `"]}]}}`))
}

func alertEvents(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Query(`SELECT "title" FROM "NotificationEvent" WHERE "type" = 'alert.fired' ORDER BY "id"`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// The episode contract end to end: breach fires once, a still-breached
// tick stays quiet (no throttle involved), clearing sends a resolution,
// and the episode state survives an engine restart.
func TestTickEpisodeFireOnceThenResolve(t *testing.T) {
	d := openAlertsTestDB(t)
	prom := &mutableProm{}
	srv := httptest.NewServer(http.HandlerFunc(prom.handler))
	t.Cleanup(srv.Close)

	mustCreateRule(t, d, db.AlertRule{
		ID: "r-5xx", Name: "5xx rate", Enabled: true, Kind: db.AlertKindHTTP5xxRate,
		Project: "shop", ThresholdFloat: f64(5), ThresholdInt: i64(20),
		WindowSeconds: 300, Severity: "error", ThrottleSeconds: 1, // tiny throttle: dedup must come from the episode, not the throttle
	})
	newEngine := func() *Engine {
		e := newTestEngine(t, d, fakeKube(shopEnvs()...))
		e.PromURL = srv.URL
		return e
	}
	e := newEngine()
	ctx := context.Background()

	prom.set("400", "100")
	e.tick(ctx)
	if got := alertEvents(t, d); len(got) != 1 || !strings.Contains(got[0], "5xx rate") {
		t.Fatalf("after breach: events = %v, want one fire", got)
	}

	time.Sleep(1100 * time.Millisecond) // past the 1s throttle
	e.tick(ctx)
	newEngine().tick(ctx) // a restarted engine must read the open episode from the DB
	if got := alertEvents(t, d); len(got) != 1 {
		t.Fatalf("ongoing episode re-fired: %v", got)
	}

	prom.set("400", "0")
	e.tick(ctx)
	got := alertEvents(t, d)
	if len(got) != 2 || !strings.HasPrefix(got[1], "✓ Resolved · 5xx rate") {
		t.Fatalf("after clear: events = %v, want fire + resolved", got)
	}
	rules, _ := d.ListAlertRules(ctx)
	if rules[0].FiringSince != nil {
		t.Errorf("episode still open after resolve: %v", rules[0].FiringSince)
	}

	e.tick(ctx)
	if got := alertEvents(t, d); len(got) != 2 {
		t.Errorf("quiet tick after resolve emitted: %v", got)
	}
}

// A long throttle must not hide the clear edge: episodic rules evaluate
// every tick, and the throttle only delays a re-fire.
func TestTickEpisodeResolvesInsideThrottle(t *testing.T) {
	d := openAlertsTestDB(t)
	prom := &mutableProm{}
	srv := httptest.NewServer(http.HandlerFunc(prom.handler))
	t.Cleanup(srv.Close)
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-5xx", Name: "5xx rate", Enabled: true, Kind: db.AlertKindHTTP5xxRate,
		Project: "shop", ThresholdFloat: f64(5), ThresholdInt: i64(20),
		WindowSeconds: 300, Severity: "warn", ThrottleSeconds: 3600,
	})
	e := newTestEngine(t, d, fakeKube(shopEnvs()...))
	e.PromURL = srv.URL
	ctx := context.Background()

	prom.set("400", "100")
	e.tick(ctx)
	prom.set("400", "0")
	e.tick(ctx)
	got := alertEvents(t, d)
	if len(got) != 2 || !strings.HasPrefix(got[1], "✓ Resolved") {
		t.Fatalf("events = %v, want fire + resolved inside the throttle window", got)
	}

	// Re-breach inside the throttle: held as a flap, not paged.
	prom.set("400", "100")
	e.tick(ctx)
	if got := alertEvents(t, d); len(got) != 2 {
		t.Errorf("flap inside throttle paged again: %v", got)
	}
}
