package db

import (
	"context"
	"testing"
	"time"
)

func findRule(t *testing.T, d *DB, id string) *AlertRule {
	t.Helper()
	rules, err := d.ListAlertRules(context.Background())
	if err != nil {
		t.Fatalf("ListAlertRules: %v", err)
	}
	for i := range rules {
		if rules[i].ID == id {
			return &rules[i]
		}
	}
	return nil
}

// Env scope and the episode columns must round-trip; a rule that loses
// firingSince across a restart would re-page for an ongoing episode.
func TestAlertRuleEnvAndEpisodeRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	pct := 5.0
	if err := d.CreateAlertRule(ctx, AlertRule{
		ID: "r1", Name: "5xx", Enabled: true, Kind: AlertKindHTTP5xxRate,
		Project: "p", Service: "web", Env: "production", ThresholdFloat: &pct,
		WindowSeconds: 300, Severity: "warn", ThrottleSeconds: 600,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	r := findRule(t, d, "r1")
	if r == nil || r.Env != "production" || r.FiringSince != nil || len(r.FiringTargets) != 0 {
		t.Fatalf("fresh rule = %+v", r)
	}

	since := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.SetAlertEpisode(ctx, "r1", &since, []string{"p-web-production", "p-web-staging"}, &since); err != nil {
		t.Fatalf("SetAlertEpisode: %v", err)
	}
	r = findRule(t, d, "r1")
	if r.FiringSince == nil || !r.FiringSince.Equal(since) {
		t.Errorf("firingSince = %v, want %v", r.FiringSince, since)
	}
	if len(r.FiringTargets) != 2 || r.FiringTargets[0] != "p-web-production" || r.FiringTargets[1] != "p-web-staging" {
		t.Errorf("firingTargets = %v", r.FiringTargets)
	}
	if r.LastFiredAt == nil || !r.LastFiredAt.Equal(since) {
		t.Errorf("lastFiredAt = %v, want %v", r.LastFiredAt, since)
	}

	// Resolving clears the episode but keeps lastFiredAt (cooldown).
	if err := d.SetAlertEpisode(ctx, "r1", nil, nil, nil); err != nil {
		t.Fatalf("clear episode: %v", err)
	}
	r = findRule(t, d, "r1")
	if r.FiringSince != nil || len(r.FiringTargets) != 0 {
		t.Errorf("episode not cleared: %+v", r)
	}
	if r.LastFiredAt == nil || !r.LastFiredAt.Equal(since) {
		t.Errorf("clearing the episode must keep lastFiredAt, got %v", r.LastFiredAt)
	}
}

// The seed migration adds the instance-wide cert rule once, and doesn't
// duplicate it (or resurrect a deleted one) on a later boot.
func TestSeedCertExpiryMigration(t *testing.T) {
	d := openTestDB(t) // truncates AlertRule
	ctx := context.Background()
	rerun := func() {
		t.Helper()
		if _, err := d.ExecContext(ctx, `DELETE FROM "SchemaMigration" WHERE "version" = 13`); err != nil {
			t.Fatalf("unrecord 0013: %v", err)
		}
		if err := d.runMigrations(ctx); err != nil {
			t.Fatalf("runMigrations: %v", err)
		}
	}
	countCert := func() int {
		t.Helper()
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "AlertRule" WHERE "kind" = 'cert_expiry'`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	rerun()
	if n := countCert(); n != 1 {
		t.Fatalf("after seed: cert_expiry rules = %d, want 1", n)
	}
	r := findRule(t, d, "default-cert-expiry")
	if r == nil || !r.Enabled || r.Project != "" || r.ThresholdInt == nil || *r.ThresholdInt != 14 {
		t.Fatalf("seeded rule = %+v", r)
	}

	// Second pass (e.g. the row was recorded then re-run) must not add another.
	rerun()
	if n := countCert(); n != 1 {
		t.Errorf("re-run duplicated the seed: %d rules", n)
	}

	// An operator-made cert rule counts as "already has one".
	if _, err := d.ExecContext(ctx, `DELETE FROM "AlertRule"`); err != nil {
		t.Fatal(err)
	}
	days := int64(7)
	if err := d.CreateAlertRule(ctx, AlertRule{ID: "mine", Name: "certs", Enabled: true, Kind: AlertKindCertExpiry,
		ThresholdInt: &days, WindowSeconds: 300, Severity: "error", ThrottleSeconds: 600}); err != nil {
		t.Fatal(err)
	}
	rerun()
	if n := countCert(); n != 1 {
		t.Errorf("seed ignored an existing cert rule: %d rules", n)
	}
}
