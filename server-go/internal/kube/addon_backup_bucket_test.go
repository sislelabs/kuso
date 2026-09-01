package kube

import "testing"

// BackupBucket is the single resolution point every backup read path
// (list, restore, pre-deploy snapshot) and the CronJob's BUCKET env go
// through. If they ever disagree, dumps get written to one bucket and
// looked for in another — which surfaces as "no backups found" for an
// addon that is in fact being backed up, or a restore that can't find
// its artifact mid-incident. Pin the semantics here.
func TestAddonBackupBucket(t *testing.T) {
	tests := []struct {
		name           string
		addon          *KusoAddon
		instanceBucket string
		want           string
	}{
		{
			name:           "nil addon falls back to instance bucket",
			addon:          nil,
			instanceBucket: "kuso-instance",
			want:           "kuso-instance",
		},
		{
			name:           "no backup block falls back",
			addon:          &KusoAddon{},
			instanceBucket: "kuso-instance",
			want:           "kuso-instance",
		},
		{
			name:           "backup block without bucket falls back",
			addon:          &KusoAddon{Spec: KusoAddonSpec{Backup: &KusoBackup{Schedule: "0 3 * * *"}}},
			instanceBucket: "kuso-instance",
			want:           "kuso-instance",
		},
		{
			name:           "empty bucket string falls back",
			addon:          &KusoAddon{Spec: KusoAddonSpec{Backup: &KusoBackup{Bucket: ""}}},
			instanceBucket: "kuso-instance",
			want:           "kuso-instance",
		},
		{
			name:           "override wins over instance bucket",
			addon:          &KusoAddon{Spec: KusoAddonSpec{Backup: &KusoBackup{Bucket: "tickero-backups"}}},
			instanceBucket: "kuso-instance",
			want:           "tickero-backups",
		},
		{
			// Callers that only care about the override (restore env,
			// snapshot env) pass "" and expect "" when unset, so the
			// secretKeyRef fallback stays in play.
			name:           "override with empty fallback returns empty",
			addon:          &KusoAddon{Spec: KusoAddonSpec{Backup: &KusoBackup{}}},
			instanceBucket: "",
			want:           "",
		},
		{
			name:           "override returned even with empty fallback",
			addon:          &KusoAddon{Spec: KusoAddonSpec{Backup: &KusoBackup{Bucket: "tickero-backups"}}},
			instanceBucket: "",
			want:           "tickero-backups",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.addon.BackupBucket(tc.instanceBucket); got != tc.want {
				t.Errorf("BackupBucket(%q) = %q, want %q", tc.instanceBucket, got, tc.want)
			}
		})
	}
}
