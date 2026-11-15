package vulnscan

import (
	"testing"
	"time"

	"kuso/server/internal/db"
)

// DB-8: the default auto-update window and the default addon backup
// schedule (addons.DefaultBackupSchedule, 03:00 UTC) both started at 03:00,
// so a Postgres roll could kill the night's dump. No minute from an hour
// before the backup to two hours after it may fall in the default window.
func TestDefaultUpdateWindowAvoidsBackupHour(t *testing.T) {
	hour := db.DefaultVulnScanSettings().NightlyHourUTC
	backup := time.Date(2026, 11, 15, 3, 0, 0, 0, time.UTC)
	for at := backup.Add(-time.Hour); at.Before(backup.Add(2 * time.Hour)); at = at.Add(time.Minute) {
		if inUpdateWindow(at, hour) {
			t.Fatalf("default window (hour %d) is open at %s, during the default 03:00 backups", hour, at.Format("15:04"))
		}
	}
}
