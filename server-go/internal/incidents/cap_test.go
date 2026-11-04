package incidents

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

type fixedConfig db.IncidentAgentConfig

func (c fixedConfig) Get(context.Context) db.IncidentAgentConfig { return db.IncidentAgentConfig(c) }

// A storm of distinct targets handled concurrently must not open more
// incidents than MaxConcurrent.
func TestHandle_StormRespectsMaxConcurrent(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `TRUNCATE TABLE "Incident" CASCADE`); err != nil {
		t.Fatal(err)
	}
	cfg := db.DefaultIncidentAgentConfig()
	cfg.Enabled, cfg.TriggerNode, cfg.MaxConcurrent = true, true, 2
	m := &Manager{DB: d, Config: fixedConfig(cfg)}

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.handle(ctx, notify.NodeUnreachable(fmt.Sprintf("node-%d", i), "NotReady", 0, 0))
		}(i)
	}
	wg.Wait()
	n, err := d.CountOpenIncidents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("open incidents = %d, want MaxConcurrent=2", n)
	}
}

// LOOP-05: incidents waiting on a human (awaiting_feedback / pr_open) run
// no agent, so they must not hold MaxConcurrent slots; and ones nobody
// touches for a week are aged out instead of soaking up attaches forever.
func TestIdleIncidentsReleaseSlots(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `TRUNCATE TABLE "Incident" CASCADE`); err != nil {
		t.Fatal(err)
	}
	idle := []string{db.IncidentAwaitingFeedback, db.IncidentPROpen, db.IncidentAwaitingFeedback}
	for i, st := range idle {
		in := db.Incident{
			ID: fmt.Sprintf("inc-idle-%d", i), EventType: "pod.crashed", Project: "p",
			Service: fmt.Sprintf("s%d", i), TargetKey: fmt.Sprintf("pod.crashed|p|s%d", i),
			State: st, Title: "t", Severity: "warn", AgentToken: "tok",
		}
		if err := d.CreateIncident(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	cfg := db.DefaultIncidentAgentConfig()
	cfg.Enabled, cfg.TriggerNode, cfg.MaxConcurrent = true, true, 3
	m := &Manager{DB: d, Config: fixedConfig(cfg)}
	m.handle(ctx, notify.NodeUnreachable("node-x", "NotReady", 0, 0))
	if n, err := d.CountActiveIncidents(ctx); err != nil || n != 1 {
		t.Fatalf("active incidents = %d, %v; new event was cap-dropped behind 3 idle incidents", n, err)
	}

	// Age one idle incident past the TTL; the reaper closes only that one.
	if _, err := d.ExecContext(ctx, `UPDATE "Incident" SET "updatedAt" = now() - interval '8 days' WHERE "id" = 'inc-idle-1'`); err != nil {
		t.Fatal(err)
	}
	m.reapIdleOpen(ctx)
	got, _ := d.GetIncident(ctx, "inc-idle-1")
	if got.State != db.IncidentTimedOut {
		t.Errorf("stale pr_open state = %q, want %q", got.State, db.IncidentTimedOut)
	}
	if fresh, _ := d.GetIncident(ctx, "inc-idle-0"); fresh.State != db.IncidentAwaitingFeedback {
		t.Errorf("fresh awaiting_feedback state = %q, must be untouched", fresh.State)
	}
}
