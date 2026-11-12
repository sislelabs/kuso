package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// A webhook URL is the channel's secret. A test send's result goes back to
// project editors, so neither the URL nor the receiver's reply (which may
// echo it) may appear in it.
func TestDeliverNowNeverReturnsChannelSecrets(t *testing.T) {
	t.Parallel()
	const token = "SECRETTOKEN-9f8e7d6c5b4a"
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Invalid Webhook Token `+r.URL.Path+`"}`)
	}))
	defer echo.Close()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := closed.Addr().String()
	_ = closed.Close()

	d := &Dispatcher{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &http.Client{Timeout: 2 * time.Second}}
	d.notifsCache = []db.Notification{
		{ID: "a", Name: "discord", Enabled: true, Type: "discord", Config: map[string]any{"url": echo.URL + "/api/webhooks/1/" + token}},
		{ID: "b", Name: "slack", Enabled: true, Type: "slack", Config: map[string]any{"url": "http://" + deadAddr + "/services/" + token}},
		{ID: "c", Name: "hook", Enabled: true, Type: "webhook", Config: map[string]any{"url": "http://" + token + ".invalid/hook"}},
	}
	d.notifsExpires = time.Now().Add(time.Hour)

	results, _, err := d.DeliverNow(context.Background(), AlertFired("r", "b", "warn", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(results)
	if strings.Contains(string(b), token) || strings.Contains(string(b), "://") {
		t.Fatalf("result leaks channel details: %s", b)
	}
	want := map[string]string{"a": "receiver returned HTTP 401", "b": "couldn't connect", "c": "DNS lookup failed"}
	for _, r := range results {
		if r.OK || r.Error != want[r.Channel] {
			t.Errorf("%s: ok=%v error=%q, want %q", r.Channel, r.OK, r.Error, want[r.Channel])
		}
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
}

func TestSafeDeliveryErrorDropsText(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`post: Post "https://hooks.slack.com/services/T0/B0/` + "xyzSECRET" + `": dial tcp: lookup hooks.slack.com: no such host`,
		`upstream 404: {"url":"https://discord.com/api/webhooks/1/xyzSECRET"}`,
		`something odd happened at https://x/xyzSECRET`,
	} {
		if got := SafeDeliveryError(errString(raw)); strings.Contains(got, "xyzSECRET") || strings.Contains(got, "https") {
			t.Errorf("%q → %q", raw, got)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// OPS-8: postSync's raw error goes into the outbox lastError column and
// the dead-letter log, so it must not carry the webhook URL either.
func TestPostSyncErrorOmitsURL(t *testing.T) {
	t.Parallel()
	const token = "SECRETTOKEN-1a2b3c"
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := closed.Addr().String()
	_ = closed.Close()
	d := &Dispatcher{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), client: &http.Client{Timeout: 2 * time.Second}}
	for _, target := range []string{
		"http://" + deadAddr + "/api/webhooks/1/" + token,
		"http://" + deadAddr + "/hook?token=" + token,
		"http://[bad/" + token,
	} {
		err := d.postSync(context.Background(), target, map[string]string{}, nil)
		if err == nil {
			t.Fatalf("%s: want an error", target)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error leaks the URL: %v", err)
		}
	}
	// The cause is kept, so the category mapping still works.
	err := d.postSync(context.Background(), "http://"+deadAddr+"/x/"+token, map[string]string{}, nil)
	if got := SafeDeliveryError(err); got != "couldn't connect" {
		t.Errorf("SafeDeliveryError = %q, want couldn't connect", got)
	}
}
