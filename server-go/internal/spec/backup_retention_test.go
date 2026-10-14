package spec

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func intPtr(v int) *int { return &v }

// RetentionDays 0 now means "keep forever", so a kuso.yml that sets a backup
// schedule without retentionDays must not send 0 (it used to always send
// &retention, turning the 14-day default into keep-forever).
func TestAddonBackupUpdateReq_RetentionOnlyWhenSpecified(t *testing.T) {
	t.Parallel()
	var a AddonSpec
	if err := yaml.Unmarshal([]byte("name: db\nkind: postgres\nbackup:\n  schedule: \"0 3 * * *\"\n"), &a); err != nil {
		t.Fatal(err)
	}
	req := addonBackupUpdateReq(a)
	if req.Backup.RetentionDays != nil {
		t.Errorf("omitted retentionDays sent as %d", *req.Backup.RetentionDays)
	}

	if err := yaml.Unmarshal([]byte("name: db\nkind: postgres\nbackup:\n  schedule: \"0 3 * * *\"\n  retentionDays: 0\n"), &a); err != nil {
		t.Fatal(err)
	}
	req = addonBackupUpdateReq(a)
	if req.Backup.RetentionDays == nil || *req.Backup.RetentionDays != 0 {
		t.Errorf("explicit retentionDays: 0 (keep forever) not sent: %v", req.Backup.RetentionDays)
	}

	// Export of a keep-forever addon must round-trip the explicit 0.
	out, err := yaml.Marshal(&AddonBackupSpec{Schedule: "0 3 * * *", RetentionDays: intPtr(0)})
	if err != nil {
		t.Fatal(err)
	}
	var back AddonBackupSpec
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.RetentionDays == nil || *back.RetentionDays != 0 {
		t.Errorf("retentionDays: 0 lost in YAML round-trip: %s", out)
	}
}
