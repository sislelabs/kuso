package db

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// SEC-7: a WebSocket ticket opens one socket across every replica. Two
// replicas share one DB, so concurrent consumes must yield exactly one
// winner.
func TestConsumeWSTicket_SingleUseAcrossReplicas(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	jti := fmt.Sprintf("jti-%d", time.Now().UnixNano())
	exp := time.Now().Add(time.Minute)

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := d.ConsumeWSTicket(ctx, jti, exp)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d consumes succeeded, want exactly 1", wins)
	}
	if ok, _ := d.ConsumeWSTicket(ctx, "", exp); ok {
		t.Error("empty jti accepted")
	}
}
