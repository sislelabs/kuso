package failures

import "testing"

// Live, berivangold 2026-09-29: a running pod whose start script ran
// `prisma migrate deploy` crashed on Prisma's advisory-lock timeout and
// pnpm printed "ELIFECYCLE Command failed with exit code 1". The crash
// card said "Build command exited non-zero" — a build detector applied to
// a running pod's logs. Pod crashes must not use build detectors, and the
// lock timeout deserves its own diagnosis.
func TestClassify_RuntimePrismaLockNotABuildFailure(t *testing.T) {
	lines := []string{
		"Error: P1002",
		"The database server was reached but timed out.",
		"Context: Timed out trying to acquire a postgres advisory lock (SELECT pg_advisory_lock(72707369)). Timeout: 10000ms. See https://pris.ly/d/migrate-advisory-locking for details.",
		" ELIFECYCLE  Command failed with exit code 1.",
	}
	c := Classify(lines, Signal{Reason: "CrashLoopBackOff", Runtime: true})
	if c.Kind == KindBuildCommandFailed {
		t.Fatalf("runtime crash classified as a build failure: %+v", c)
	}
	if c.Kind != KindMigrationLock || c.Remediation == nil {
		t.Errorf("kind %q remediation %v, want %q with a fix", c.Kind, c.Remediation, KindMigrationLock)
	}

	// Runtime logs without a pod-state reason (e.g. a pod that exited
	// before the kubelet reported a waiting reason) must not reach the
	// build detectors either.
	if r := Classify(lines, Signal{Runtime: true}); r.Kind == KindBuildCommandFailed {
		t.Errorf("runtime logs without a reason classified as a build failure: %+v", r)
	}

	// A real build log still classifies as a build command failure.
	b := Classify([]string{"ERROR: failed to solve: process \"/bin/sh -c pnpm build\" did not complete successfully", " ELIFECYCLE  Command failed with exit code 1."}, Signal{})
	if b.Kind != KindBuildCommandFailed {
		t.Errorf("build log kind %q, want %q", b.Kind, KindBuildCommandFailed)
	}
}
