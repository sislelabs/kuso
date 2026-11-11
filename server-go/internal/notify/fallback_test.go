package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/db"
)

// With the control-plane DB down, a platform event must still reach the
// channels (and EmitDurable must say so), while ordinary events keep
// failing so their callers retry through the outbox.
func TestEmitDurable_DirectFallbackWhenOutboxDown(t *testing.T) {
	d := openNotifyTestDB(t)
	addChannel(t, d, "ops", nil)

	prev := directFallbackBackoff
	directFallbackBackoff = time.Millisecond
	t.Cleanup(func() { directFallbackBackoff = prev })

	disp := New(d, quietLogger(), 0)
	var mu sync.Mutex
	var sent []EventType
	fail := 0
	disp.directSendFn = func(_ context.Context, n db.Notification, e Event) error {
		mu.Lock()
		defer mu.Unlock()
		if fail > 0 {
			fail--
			return errors.New("503")
		}
		sent = append(sent, e.Type)
		return nil
	}
	// Healthy DB: the channel list is read and remembered.
	if err := disp.EmitDurable(BuildFailed("shop", "web", "abc", "boom")); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("healthy outbox used the direct path: %v", sent)
	}

	_ = d.Close() // the outage

	if err := disp.EmitDurable(BuildFailed("shop", "web", "abc", "boom")); err == nil {
		t.Fatal("non-platform event reported delivered while the outbox is down")
	}
	fail = 1 // first attempt fails, the retry lands
	if err := disp.EmitDurable(PlatformUnhealthy("pgbouncer", "kuso", "no ready pod", time.Now())); err != nil {
		t.Fatalf("platform event during DB outage: %v", err)
	}
	if len(sent) != 1 || sent[0] != EventPlatformUnhealthy {
		t.Fatalf("direct sends = %v, want one platform.unhealthy", sent)
	}

	fail = directFallbackAttempts
	if err := disp.EmitDurable(PlatformUnhealthy("pgbouncer", "kuso", "no ready pod", time.Now())); err == nil {
		t.Fatal("every direct attempt failed but EmitDurable reported success")
	}
}

// OPS-8: a kuso-server that restarts while Postgres is down has never
// read the channel list; the copy kept in a Secret must still page.
func TestDirectFallbackSurvivesRestartDuringOutage(t *testing.T) {
	d := openNotifyTestDB(t)
	addChannel(t, d, "ops", nil)
	store := &SecretChannelSnapshot{Client: kubefake.NewSimpleClientset(), Namespace: "kuso"}

	before := New(d, quietLogger(), 0)
	before.SetChannelSnapshotStore(context.Background(), store)
	if err := before.EmitDurable(BuildFailed("shop", "web", "abc", "boom")); err != nil {
		t.Fatal(err)
	}
	before.saveSnapshot() // the async save, made deterministic

	_ = d.Close() // the outage, then the restart
	after := New(d, quietLogger(), 0)
	var sent []string
	after.directSendFn = func(_ context.Context, n db.Notification, e Event) error {
		sent = append(sent, n.ID)
		return nil
	}
	after.SetChannelSnapshotStore(context.Background(), store)
	if err := after.EmitDurable(PlatformUnhealthy("kuso-postgres", "kuso", "no pod", time.Now())); err != nil {
		t.Fatalf("platform event after a restart during the outage: %v", err)
	}
	if len(sent) != 1 || sent[0] != "ops" {
		t.Fatalf("direct sends = %v, want the stored channel", sent)
	}
}
