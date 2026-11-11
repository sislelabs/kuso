package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// SEC-7: with a shared consumer wired, a ticket redeemed on one replica
// is spent on every other; a store error refuses the ticket.
func TestConsumeWSTicket_SharedAcrossReplicas(t *testing.T) {
	var mu sync.Mutex
	used := map[string]bool{}
	shared := func(_ context.Context, jti string, _ time.Time) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if used[jti] {
			return false, nil
		}
		used[jti] = true
		return true, nil
	}
	a, _ := NewIssuer("s", time.Hour)
	b, _ := NewIssuer("s", time.Hour)
	a.SetTicketConsumer(shared)
	b.SetTicketConsumer(shared)
	ctx := context.Background()
	if !a.ConsumeWSTicket(ctx, "t1", time.Time{}) {
		t.Fatal("first use refused")
	}
	if b.ConsumeWSTicket(ctx, "t1", time.Time{}) {
		t.Fatal("ticket replayed on a second replica")
	}
	b.SetTicketConsumer(func(context.Context, string, time.Time) (bool, error) { return true, errors.New("db down") })
	if b.ConsumeWSTicket(ctx, "t2", time.Time{}) {
		t.Fatal("store error accepted the ticket")
	}
}
