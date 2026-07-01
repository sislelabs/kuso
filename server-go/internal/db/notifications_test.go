package db

import (
	"context"
	"testing"
)

// TestNotification_MentionsRoundTrip is the regression guard for the
// bug where a Discord channel's per-event mention rules were dropped on
// save: configCols only persisted the typed URL column, so an explicit
// "none" (whose absence reverts an error event to the @here default)
// never reached the DB and reverted on reload. PG-backed; skips without
// KUSO_TEST_PG_DSN.
func TestNotification_MentionsRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	mentions := map[string]any{
		"backup.failed": "none",      // explicit opt-out over the @here default
		"build.failed":  "@everyone", // explicit escalation
	}
	n := &Notification{
		ID:      "notif-mentions-test",
		Name:    "discord-test",
		Enabled: true,
		Type:    "discord",
		Events:  []string{"backup.failed", "build.failed"},
		Config: map[string]any{
			"url":      "https://discord.com/api/webhooks/1/abc",
			"mentions": mentions,
		},
	}
	t.Cleanup(func() { _ = d.DeleteNotification(ctx, n.ID) })

	if err := d.CreateNotification(ctx, n); err != nil {
		t.Fatalf("CreateNotification: %v", err)
	}

	got, err := d.FindNotification(ctx, n.ID)
	if err != nil {
		t.Fatalf("FindNotification: %v", err)
	}
	m, ok := got.Config["mentions"].(map[string]any)
	if !ok {
		t.Fatalf("mentions not persisted: Config=%v", got.Config)
	}
	if m["backup.failed"] != "none" {
		t.Errorf("backup.failed mention = %v, want none (explicit opt-out lost)", m["backup.failed"])
	}
	if m["build.failed"] != "@everyone" {
		t.Errorf("build.failed mention = %v, want @everyone", m["build.failed"])
	}

	// Update path: change the rule, confirm it persists too.
	n.Config["mentions"] = map[string]any{"backup.failed": "@here"}
	if err := d.UpdateNotification(ctx, n); err != nil {
		t.Fatalf("UpdateNotification: %v", err)
	}
	got2, _ := d.FindNotification(ctx, n.ID)
	m2, _ := got2.Config["mentions"].(map[string]any)
	if m2["backup.failed"] != "@here" {
		t.Errorf("after update: backup.failed = %v, want @here", m2["backup.failed"])
	}
	if _, stale := m2["build.failed"]; stale {
		t.Errorf("after update: build.failed should be gone (wholesale replace), got %v", m2["build.failed"])
	}
}

// TestNotification_ChannelConfigRoundTrip is the regression guard for
// M14: telegram / pushover / mattermost / email config (bot tokens,
// chat ids, SMTP creds) had no persisted home — configCols only wrote
// the typed webhook/slack/discord columns — so at delivery time
// deliverViaChannel saw empty config and every send dead-lettered. The
// generic "config" JSON column now carries the full map for all types.
func TestNotification_ChannelConfigRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	cases := []struct {
		id   string
		typ  string
		cfg  map[string]any
		want map[string]string // key → expected string value after reload
	}{
		{
			id:  "notif-telegram",
			typ: "telegram",
			cfg: map[string]any{"botToken": "123456:AAtoken", "chatId": "-1001234"},
			want: map[string]string{
				"botToken": "123456:AAtoken",
				"chatId":   "-1001234",
			},
		},
		{
			id:  "notif-pushover",
			typ: "pushover",
			cfg: map[string]any{"token": "apptoken", "user": "userkey"},
			want: map[string]string{
				"token": "apptoken",
				"user":  "userkey",
			},
		},
		{
			id:  "notif-email",
			typ: "email",
			cfg: map[string]any{
				"host": "smtp.example.com", "port": "587",
				"username": "u", "password": "p",
				"from": "a@b.c", "to": "x@y.z",
			},
			want: map[string]string{
				"host": "smtp.example.com", "port": "587",
				"username": "u", "password": "p",
				"from": "a@b.c", "to": "x@y.z",
			},
		},
		{
			id:  "notif-mattermost",
			typ: "mattermost",
			cfg: map[string]any{"url": "https://mm.example.com/hooks/abc"},
			want: map[string]string{
				"url": "https://mm.example.com/hooks/abc",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			n := &Notification{
				ID: c.id, Name: c.typ + "-test", Enabled: true,
				Type: c.typ, Events: []string{"build.failed"}, Config: c.cfg,
			}
			t.Cleanup(func() { _ = d.DeleteNotification(ctx, n.ID) })
			if err := d.CreateNotification(ctx, n); err != nil {
				t.Fatalf("CreateNotification: %v", err)
			}
			got, err := d.FindNotification(ctx, n.ID)
			if err != nil {
				t.Fatalf("FindNotification: %v", err)
			}
			for k, want := range c.want {
				if v, _ := got.Config[k].(string); v != want {
					t.Errorf("%s: Config[%q] = %q, want %q (config not persisted → delivery dead-letters)", c.typ, k, v, want)
				}
			}
		})
	}
}
