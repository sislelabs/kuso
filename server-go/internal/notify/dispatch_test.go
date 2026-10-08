package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"kuso/server/internal/db"
)

func TestProjectMatches(t *testing.T) {
	cases := []struct {
		name      string
		project   string
		whitelist []string
		want      bool
	}{
		{"empty whitelist admits all", "shop", nil, true},
		{"listed project", "shop", []string{"blog", "shop"}, true},
		{"unlisted project", "shop", []string{"blog"}, false},
		{"project-less event always passes", "", []string{"blog"}, true},
	}
	for _, tc := range cases {
		if got := projectMatches(tc.project, tc.whitelist); got != tc.want {
			t.Errorf("%s: projectMatches(%q, %v) = %v, want %v", tc.name, tc.project, tc.whitelist, got, tc.want)
		}
	}
}

// A cross-project uptime summary must not reach a channel scoped to one
// client project; that channel gets the per-project twin instead.
func TestChannelAdmitsAudience(t *testing.T) {
	all := db.Notification{Enabled: true, Type: "discord"}
	shopOnly := db.Notification{Enabled: true, Type: "discord", Pipelines: []string{"shop"}}
	cluster := UptimeDownCluster([]UptimeTarget{{Project: "a", Service: "web"}, {Project: "b", Service: "web"}, {Project: "shop", Service: "web"}})
	twin := UptimeDown("shop", []UptimeTarget{{Project: "shop", Service: "web"}})
	twin.Audience = AudienceScoped
	cases := []struct {
		name string
		n    db.Notification
		e    Event
		want bool
	}{
		{"cluster summary to unscoped channel", all, cluster, true},
		{"cluster summary to scoped channel", shopOnly, cluster, false},
		{"twin to scoped channel", shopOnly, twin, true},
		{"twin to unscoped channel (already has the summary)", all, twin, false},
		{"plain project-less event to scoped channel", shopOnly, NodeRecovered("n1", 0), true},
	}
	for _, c := range cases {
		if got := channelAdmits(c.n, c.e); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCatalogueMatchesAllEventTypes(t *testing.T) {
	inAll := map[EventType]bool{}
	for _, e := range AllEventTypes {
		inAll[e] = true
	}
	if len(AllEventTypes) != len(EventCatalogue)+1 || !inAll[EventTestPing] {
		t.Fatalf("AllEventTypes should be the catalogue + test.ping, got %v", AllEventTypes)
	}
	for _, dead := range []EventType{EventBuildStarted, EventDeployRolled} {
		if inAll[dead] {
			t.Errorf("%s is never emitted and must not be listed", dead)
		}
	}
	if !inAll[EventAddonCrashed] || !inAll[EventNodeUpdatesApplied] {
		t.Error("addon.crashed / node.updates-applied missing")
	}
}

// openNotifyTestDB returns a truncated Postgres from KUSO_TEST_PG_DSN or
// skips, mirroring the db package's helper.
func openNotifyTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := d.DB.Exec(`TRUNCATE TABLE "NotificationOutbox", "NotificationEvent",
		"ProjectNotificationMute", "Notification" RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func addChannel(t *testing.T, d *db.DB, id string, projects []string) {
	t.Helper()
	if err := d.CreateNotification(context.Background(), &db.Notification{
		ID: id, Name: id, Enabled: true, Type: "webhook",
		Pipelines: projects,
		Config:    map[string]any{"url": "https://hooks.example.com/" + id},
	}); err != nil {
		t.Fatalf("create channel %s: %v", id, err)
	}
}

// outboxChannels returns the channel id of every pending outbox row.
func outboxChannels(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.DB.Query(`SELECT "notificationId" FROM "NotificationOutbox" WHERE "deliveredAt" IS NULL ORDER BY id`)
	if err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// Emit must write outbox rows itself — Run is never started here — and
// honour each channel's project whitelist.
func TestEmit_EnqueuesOutboxWithoutRun(t *testing.T) {
	d := openNotifyTestDB(t)
	addChannel(t, d, "all", nil)
	addChannel(t, d, "shop-only", []string{"shop"})
	addChannel(t, d, "blog-only", []string{"blog"})

	disp := New(d, quietLogger(), 0)
	disp.Emit(BuildFailed("shop", "web", "abc", "boom"))

	got := outboxChannels(t, d)
	want := []string{"all", "shop-only"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("outbox rows for channels %v, want %v", got, want)
	}

	// Project-less event reaches every channel regardless of whitelist.
	disp.Emit(NodeRecovered("node-1", 0))
	if n := len(outboxChannels(t, d)); n != 5 {
		t.Fatalf("after node event: %d pending rows, want 5", n)
	}
}

func TestEmit_UptimeStormSplitsByAudience(t *testing.T) {
	d := openNotifyTestDB(t)
	addChannel(t, d, "all", nil)
	addChannel(t, d, "shop-only", []string{"shop"})
	disp := New(d, quietLogger(), 0)

	cluster := UptimeDownCluster([]UptimeTarget{{Project: "a", Service: "web"}, {Project: "b", Service: "web"}, {Project: "shop", Service: "web"}})
	if err := disp.EmitDurable(cluster); err != nil {
		t.Fatal(err)
	}
	if got := outboxChannels(t, d); len(got) != 1 || got[0] != "all" {
		t.Fatalf("cluster summary rows = %v, want [all]", got)
	}
	twin := UptimeDown("shop", []UptimeTarget{{Project: "shop", Service: "web"}})
	twin.Audience = AudienceScoped
	if err := disp.EmitDurable(twin); err != nil {
		t.Fatal(err)
	}
	if got := outboxChannels(t, d); len(got) != 2 || got[1] != "shop-only" {
		t.Fatalf("after twin rows = %v, want [all shop-only]", got)
	}
	var feed int
	if err := d.DB.QueryRow(`SELECT count(*) FROM "NotificationEvent" WHERE coalesce(project, '') = ''`).Scan(&feed); err != nil {
		t.Fatal(err)
	}
	if feed != 0 {
		t.Fatalf("cluster summary landed in the bell feed as a project-less row (%d)", feed)
	}
}

func claimOne(t *testing.T, d *db.DB) *db.OutboxRow {
	t.Helper()
	row, err := d.ClaimOutboxRow(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return row
}

func isDelivered(t *testing.T, d *db.DB, id int64) bool {
	t.Helper()
	var delivered bool
	if err := d.DB.QueryRow(`SELECT "deliveredAt" IS NOT NULL FROM "NotificationOutbox" WHERE id = $1`, id).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	return delivered
}

// A transient channel-lookup failure must leave the row for retry; only
// a genuinely missing channel drops it.
func TestDrainOne_LookupErrorHandling(t *testing.T) {
	d := openNotifyTestDB(t)
	ctx := context.Background()
	disp := New(d, quietLogger(), 0)
	payload, _ := db.MarshalOutboxPayload(BuildFailed("shop", "web", "abc", "boom"))

	id1, _ := d.EnqueueOutbox(ctx, "chan-x", string(EventBuildFailed), payload)
	disp.lookupChannelFn = func(context.Context, string) (db.Notification, error) {
		return db.Notification{}, errors.New("db: connection reset")
	}
	processed, err := disp.drainOne(ctx)
	if !processed || err == nil {
		t.Fatalf("transient lookup: processed=%v err=%v, want processed with error", processed, err)
	}
	if isDelivered(t, d, id1) {
		t.Fatal("transient lookup error dropped the row")
	}

	// Real lookup: channel id with no row in the DB (cache empty too).
	id2, _ := d.EnqueueOutbox(ctx, "chan-gone", string(EventBuildFailed), payload)
	disp.lookupChannelFn = nil
	if _, err := disp.drainOne(ctx); err != nil {
		t.Fatalf("missing channel: %v", err)
	}
	if !isDelivered(t, d, id2) {
		t.Fatal("row for a deleted channel should be dropped (marked delivered)")
	}
}

// A channel created after the dispatcher cached its config list (another
// replica, or a missed invalidation) must be found, not treated as deleted.
func TestLookupNotification_FallsThroughStaleCache(t *testing.T) {
	d := openNotifyTestDB(t)
	ctx := context.Background()
	disp := New(d, quietLogger(), 0)
	disp.notifsCache = []db.Notification{}
	disp.notifsExpires = time.Now().Add(time.Hour)
	addChannel(t, d, "late", nil)
	n, err := disp.lookupNotification(ctx, "late")
	if err != nil || n.ID != "late" {
		t.Fatalf("lookup late channel: %+v, %v", n, err)
	}
	if _, err := disp.lookupNotification(ctx, "nope"); !errors.Is(err, errChannelNotFound) {
		t.Fatalf("missing channel err = %v, want errChannelNotFound", err)
	}
}

// A replica without the singletons lease must still enqueue to the
// durable outbox (the leader's workers deliver it) but must not run the
// leader-only event hook.
func TestEmit_NonLeaderEnqueuesButSkipsHook(t *testing.T) {
	d := openNotifyTestDB(t)
	addChannel(t, d, "all", nil)

	disp := New(d, quietLogger(), 0)
	disp.SetLeaderHook(func() bool { return false })
	hookCalls := 0
	disp.SetEventHook(func(Event) { hookCalls++ })
	disp.Emit(BuildFailed("shop", "web", "abc", "boom"))

	if got := outboxChannels(t, d); len(got) != 1 || got[0] != "all" {
		t.Fatalf("non-leader outbox rows = %v, want [all]", got)
	}
	if hookCalls != 0 {
		t.Errorf("event hook ran %d times on a non-leader, want 0", hookCalls)
	}
}
