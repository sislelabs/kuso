package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The dead-man switch only pings while the control plane is healthy;
// a failing DB check must stop the pings so the external side pages.
func TestHeartbeatSkipsPingWhenCheckFails(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	var dbErr error
	h := &Heartbeat{URL: srv.URL, Check: func(context.Context) error { return dbErr }}
	if err := h.beat(context.Background()); err != nil || hits.Load() != 1 {
		t.Fatalf("healthy beat: err=%v hits=%d", err, hits.Load())
	}
	dbErr = errors.New("db down")
	if err := h.beat(context.Background()); err == nil || hits.Load() != 1 {
		t.Fatalf("unhealthy beat pinged anyway: err=%v hits=%d", err, hits.Load())
	}
}
