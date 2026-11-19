package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kuso/server/internal/db"
)

type outboxState struct {
	attempts      int
	lastError     string
	nextAttemptIn time.Duration
}

func readOutboxRow(t *testing.T, d *db.DB, id int64) outboxState {
	t.Helper()
	var st outboxState
	var secs float64
	if err := d.DB.QueryRow(`SELECT "attempts", COALESCE("lastError", ''),
		EXTRACT(EPOCH FROM ("nextAttemptAt" - (now() AT TIME ZONE 'UTC')))
		FROM "NotificationOutbox" WHERE id = $1`, id).Scan(&st.attempts, &st.lastError, &secs); err != nil {
		t.Fatal(err)
	}
	st.nextAttemptIn = time.Duration(secs * float64(time.Second))
	return st
}

func enqueueBuildFailed(t *testing.T, d *db.DB, channel string) int64 {
	t.Helper()
	payload, _ := db.MarshalOutboxPayload(BuildFailed("shop", "web", "abc", "boom"))
	id, err := d.EnqueueOutbox(context.Background(), channel, string(EventBuildFailed), payload)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// LOOP-1: a row whose processing panics must cost an attempt, not the
// worker, so it walks to dead-letter instead of killing every worker.
func TestDrainOne_PanicIsRecoveredAsFailedAttempt(t *testing.T) {
	d := openNotifyTestDB(t)
	ctx := context.Background()
	disp := New(d, quietLogger(), 0)
	disp.lookupChannelFn = func(context.Context, string) (db.Notification, error) {
		panic("renderer hit a nil field")
	}
	id := enqueueBuildFailed(t, d, "chan-x")

	processed, err := disp.drainOne(ctx)
	if !processed || err != nil {
		t.Fatalf("drainOne = (%v, %v), want (true, nil)", processed, err)
	}
	st := readOutboxRow(t, d, id)
	if st.attempts != 1 || st.lastError != "panic: renderer hit a nil field" {
		t.Fatalf("after panic: %+v, want attempts=1 with the panic as lastError", st)
	}

	if _, err := d.DB.Exec(`UPDATE "NotificationOutbox" SET "attempts" = $1, "nextAttemptAt" = now() - interval '1 hour' WHERE id = $2`,
		db.MaxOutboxAttempts-1, id); err != nil {
		t.Fatal(err)
	}
	if _, err := disp.drainOne(ctx); err != nil {
		t.Fatal(err)
	}
	if st := readOutboxRow(t, d, id); st.attempts < db.MaxOutboxAttempts {
		t.Fatalf("poison row not dead-lettered at the cap: %+v", st)
	}
	if _, err := d.ClaimOutboxRow(ctx); !errors.Is(err, db.ErrNoOutboxRow) {
		t.Fatalf("dead-lettered poison row is still claimable: %v", err)
	}
}

// BLD-7: a 4xx that retrying can't fix dead-letters on the first try; a
// 429 waits as long as the receiver's Retry-After asks.
func TestDrainOne_UpstreamStatusHandling(t *testing.T) {
	d := openNotifyTestDB(t)
	ctx := context.Background()
	status, retryAfter := http.StatusNotFound, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"Unknown Webhook"}`))
	}))
	defer srv.Close()
	disp := New(d, quietLogger(), 0)
	disp.client = srv.Client()
	disp.lookupChannelFn = func(context.Context, string) (db.Notification, error) {
		return db.Notification{ID: "hook", Enabled: true, Type: "webhook", Config: map[string]any{"url": srv.URL}}, nil
	}

	gone := enqueueBuildFailed(t, d, "hook")
	if _, err := disp.drainOne(ctx); err != nil {
		t.Fatal(err)
	}
	if st := readOutboxRow(t, d, gone); st.attempts < db.MaxOutboxAttempts {
		t.Fatalf("404 not dead-lettered at once: %+v", st)
	}

	status, retryAfter = http.StatusTooManyRequests, "120"
	throttled := enqueueBuildFailed(t, d, "hook")
	if _, err := disp.drainOne(ctx); err != nil {
		t.Fatal(err)
	}
	st := readOutboxRow(t, d, throttled)
	if st.attempts != 1 || st.nextAttemptIn < 110*time.Second || st.nextAttemptIn > 130*time.Second {
		t.Fatalf("429 Retry-After 120: %+v, want attempts=1 and next attempt in ~120s", st)
	}

	status, retryAfter = http.StatusBadGateway, ""
	flaky := enqueueBuildFailed(t, d, "hook")
	if _, err := disp.drainOne(ctx); err != nil {
		t.Fatal(err)
	}
	if st := readOutboxRow(t, d, flaky); st.attempts != 1 || st.nextAttemptIn > 10*time.Second {
		t.Fatalf("502: %+v, want one attempt on the normal ladder", st)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":                              0,
		"3":                             3 * time.Second,
		"1.5":                           1500 * time.Millisecond,
		"-2":                            0,
		"Thu, 19 Nov 2026 12:01:00 GMT": time.Minute,
		"garbage":                       0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
