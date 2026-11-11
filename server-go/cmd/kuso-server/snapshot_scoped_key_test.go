package main

import "testing"

// BLOB-R1: a scoped project's pre-deploy snapshot reads its own key; no env
// ref may still name the bucket-wide Secret.
func TestRetargetSecretRefs_SnapshotUsesScopedKey(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso-shop", "shop-db-snapshot-1", "shop/shop-db/k.sql.gz", "shop", "db", "pre-deploy", "abc", "", false)
	retargetSecretRefs(job.Spec.Template.Spec.Containers, snapshotBackupSecretName, "kuso-backup-s3-shop")
	var scoped int
	for _, c := range job.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				continue
			}
			switch e.ValueFrom.SecretKeyRef.Name {
			case snapshotBackupSecretName:
				t.Errorf("%s still reads %s", e.Name, snapshotBackupSecretName)
			case "kuso-backup-s3-shop":
				scoped++
			}
		}
	}
	if scoped < 4 {
		t.Errorf("only %d env refs read the scoped Secret, want bucket/endpoint/key id/secret", scoped)
	}
}
