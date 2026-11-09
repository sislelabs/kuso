package errorscan

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// hack/sandbox/seed/worker.sh end to end: its output is stored a line at
// a time and the scanner ticks after every line, so every batch and scan
// boundary falls inside a trace at some point. One traceback must stay one
// occurrence of one group, never "failed to send order receipt" plus
// "Python traceback (exception line not captured)".
func TestTick_SeedWorkerOutputGroupsIntoTwoAtEveryBoundary(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	now := t0
	s := &Scanner{DB: d, Logger: slog.Default(), now: func() time.Time { return now }}

	at := t0
	emit := func(lines ...string) {
		for i, l := range lines {
			if i > 0 {
				at = at.Add(2 * time.Millisecond) // one echo per line
			}
			insertLine(t, d, at, "shop-worker-1", l)
			now = at.Add(500 * time.Millisecond)
			s.tick(ctx)
		}
		at = at.Add(10 * time.Second)
	}
	for n := 1; n <= 24; n++ {
		stamp := at.Format(time.RFC3339)
		emit(fmt.Sprintf("%s INFO processed %d orders from queue=orders in %dms", stamp, n%20+1, n*7%200+20))
		if n%4 == 0 {
			emit(fmt.Sprintf("%s WARN redis: slow reply from cache (%dms) retrying", stamp, n*13%400+250))
		}
		if n%6 != 0 {
			continue
		}
		if n%12 == 0 {
			emit(fmt.Sprintf("%s ERROR failed to send order receipt order_id=%d", stamp, 1000+n),
				"Traceback (most recent call last):",
				`  File "/app/jobs/emails.py", line 42, in send_receipt`,
				"    smtp.send(msg)",
				"smtplib.SMTPServerDisconnected: Connection unexpectedly closed")
		} else {
			emit(fmt.Sprintf("%s ERROR payment webhook failed: stripe: read: connection reset by peer order_id=%d", stamp, 1000+n),
				"    at processWebhook (/app/jobs/payments.go:88)",
				"    at runJob (/app/queue/worker.go:141)",
				"    at main.loop (/app/main.go:52)")
		}
	}
	now = at.Add(time.Minute)
	s.tick(ctx)

	groups, err := d.ListErrorGroups(ctx, "shop", "shop-worker", "", t0.Add(-time.Hour), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, g := range groups {
		got[g.Message] = g.Count
	}
	smtp := "smtplib.SMTPServerDisconnected: Connection unexpectedly closed"
	if len(got) != 2 || got[smtp] != 2 {
		t.Fatalf("groups = %v, want the SMTP traceback and the webhook stack, 2 each", got)
	}
	if n := errorEventCount(t, d); n != 4 {
		t.Errorf("ErrorEvent rows = %d, want 4 occurrences", n)
	}
}
