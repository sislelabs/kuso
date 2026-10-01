package kube

import "testing"

// The kusoaddon chart renders the pg_dump CronJob for HA postgres too
// (against <name>-rw); health checks must expect it.
func TestAddonBackupCronJobRendered_HAPostgres(t *testing.T) {
	a := &KusoAddon{Spec: KusoAddonSpec{Kind: "postgres", HA: true, Backup: &KusoBackup{Schedule: "0 3 * * *"}}}
	if !AddonBackupCronJobRendered(a) {
		t.Fatal("HA postgres with a schedule renders a backup CronJob")
	}
}
