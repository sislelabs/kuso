package incidents

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// An incident whose implement agent died without opening a PR stayed in
// "implementing" forever, holding a MaxConcurrent slot.
func TestReapStuckImplementing(t *testing.T) {
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
	in := db.Incident{
		ID: "inc-impl", EventType: "pod.crashed", Project: "p", Service: "web",
		TargetKey: "pod.crashed|p|web", State: db.IncidentImplementing, Title: "web crashed",
		Severity: "error", ContextPack: json.RawMessage(`{}`), AgentToken: "tok",
	}
	if err := d.CreateIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := d.SetIncidentJob(ctx, in.ID, "implement", "kuso-incident-inc-impl-implement"); err != nil {
		t.Fatal(err)
	}
	// The Job is gone (TTL'd after it finished) and the incident never moved.
	m := &Manager{DB: d, Kube: &kube.Client{Clientset: fake.NewSimpleClientset()}, now: func() time.Time { return time.Now() }}
	m.reapStuckImplementing(ctx)
	got, err := d.GetIncident(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != db.IncidentTimedOut {
		t.Fatalf("state = %q, want %q", got.State, db.IncidentTimedOut)
	}
}
