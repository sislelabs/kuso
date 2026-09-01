package handlers

import (
	"testing"

	apiv1 "github.com/sislelabs/kuso/api/apiv1"
)

// apiv1UpdateAddonToDomain copies the wire PATCH shape field by field, so
// a new field on apiv1.UpdateAddonBackup that isn't added here is dropped
// silently: the API returns 200, the CR never changes, and the user sees
// their setting "not stick" with no error anywhere. That happened to
// spec.backup.bucket. Assert every field survives the hop.
func TestAPIv1UpdateAddonBackupRoundTrip(t *testing.T) {
	sched := "0 3 * * *"
	retention := 30
	bucket := "tickero-backups"

	got := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{
		Backup: &apiv1.UpdateAddonBackup{
			Schedule:      &sched,
			RetentionDays: &retention,
			Bucket:        &bucket,
		},
	})

	if got.Backup == nil {
		t.Fatal("Backup patch dropped entirely")
	}
	if got.Backup.Schedule == nil || *got.Backup.Schedule != sched {
		t.Errorf("Schedule = %v, want %q", got.Backup.Schedule, sched)
	}
	if got.Backup.RetentionDays == nil || *got.Backup.RetentionDays != retention {
		t.Errorf("RetentionDays = %v, want %d", got.Backup.RetentionDays, retention)
	}
	if got.Backup.Bucket == nil || *got.Backup.Bucket != bucket {
		t.Errorf("Bucket = %v, want %q", got.Backup.Bucket, bucket)
	}
}

// A nil Backup block must stay nil rather than becoming an empty patch —
// an empty non-nil patch would make Update lazy-init spec.backup on an
// addon that never asked for backups.
func TestAPIv1UpdateAddonBackupNilStaysNil(t *testing.T) {
	if got := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{}); got.Backup != nil {
		t.Errorf("nil Backup became %+v", got.Backup)
	}
}

// Clearing the override is "" (not nil): nil means leave alone, "" means
// go back to the instance-wide bucket. Both must survive the mapping.
func TestAPIv1UpdateAddonBackupClearBucket(t *testing.T) {
	empty := ""
	got := apiv1UpdateAddonToDomain(apiv1.UpdateAddonRequest{
		Backup: &apiv1.UpdateAddonBackup{Bucket: &empty},
	})
	if got.Backup == nil || got.Backup.Bucket == nil {
		t.Fatalf("cleared bucket dropped: %+v", got.Backup)
	}
	if *got.Backup.Bucket != "" {
		t.Errorf("Bucket = %q, want empty", *got.Backup.Bucket)
	}
}
