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
