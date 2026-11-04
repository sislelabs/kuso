package main

import "testing"

// DATA-07: an external conn may carry only DATABASE_URL. Required POSTGRES_*
// refs left the snapshot pod in CreateContainerConfigError, blocking every
// deploy behind snapshotBeforeDeploy and leaking a stuck pod per attempt.
func TestBuildSnapshotJob_ExternalUsesOptionalDSNAndDeadline(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso", "p-psdb-snapshot-1", "k", "p", "psdb", "t", "b", "", true)
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds <= 0 {
		t.Fatal("snapshot Job has no activeDeadlineSeconds")
	}
	seen := map[string]bool{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		seen[e.Name] = true
		if ref := e.ValueFrom; ref != nil && ref.SecretKeyRef != nil && ref.SecretKeyRef.Name == "p-psdb-conn" {
			if ref.SecretKeyRef.Optional == nil || !*ref.SecretKeyRef.Optional {
				t.Errorf("%s is a required ref into the external conn", e.Name)
			}
		}
	}
	if !seen["EXTERNAL_SNAPSHOT"] || !seen["SNAP_DATABASE_URL"] {
		t.Errorf("external snapshot env missing DSN wiring: %v", seen)
	}
}
