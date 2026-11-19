package db

import (
	"context"
	"testing"
)

// BLD-1: platform, ingress and alert-engine outages stayed in "Needs
// attention" after platform.recovered, since nothing paired them.
func TestNotificationAttention_PlatformRecoveryClosesEpisode(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "NotificationEvent"`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key LIKE 'notifications.readUpTo.%'`); err != nil {
		t.Fatal(err)
	}
	events := []NotificationEvent{
		{Type: "platform.unhealthy", Title: "buildkitd unhealthy", Severity: "error", Extra: map[string]string{"component": "buildkitd", "namespace": "kuso"}},
		{Type: "platform.unhealthy", Title: "registry unhealthy", Severity: "error", Extra: map[string]string{"component": "registry", "namespace": "kuso"}},
		{Type: "ingress.down", Title: "ingress probe down", Severity: "error", Extra: map[string]string{"source": "probe through Traefik"}},
		{Type: "ingress.down", Title: "ingress traffic stopped", Severity: "error", Extra: map[string]string{"source": "traffic stopped"}},
		{Type: "alert.engine_degraded", Title: "alert engine degraded", Severity: "error"},
		{Type: "platform.recovered", Title: "buildkitd recovered", Severity: "info", Extra: map[string]string{"component": "buildkitd"}},
		{Type: "platform.recovered", Title: "traefik recovered", Severity: "info", Extra: map[string]string{"component": "ingress (Traefik)"}},
		{Type: "platform.recovered", Title: "prometheus recovered", Severity: "info", Extra: map[string]string{"component": "alert engine (Prometheus)"}},
	}
	for _, e := range events {
		if err := d.InsertNotificationEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	list, err := d.ListAttentionNotificationEvents(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		got = append(got, e.Title)
	}
	want := map[string]bool{"registry unhealthy": true, "ingress traffic stopped": true}
	if len(got) != len(want) {
		t.Fatalf("attention = %v, want only %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("attention = %v, want only %v", got, want)
		}
	}
	if n, _ := d.CountUnreadNotificationEventsForUser(ctx, "carol", true); n != 2 {
		t.Errorf("attention unread = %d, want 2", n)
	}
}
