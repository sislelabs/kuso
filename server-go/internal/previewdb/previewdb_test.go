package previewdb

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// TestIsPRClone locks in the DeletePRAddons ownership-label fix: a real
// project addon named "events-pr-2" (no preview labels) must NOT be
// treated as PR #2's clone and deleted when the PR closes. Selection is
// label-first; the name-suffix path is a legacy fallback gated on the env
// label.
func TestIsPRClone(t *testing.T) {
	t.Parallel()

	const prLabel = "2"
	const suffix = "-pr-2"

	addon := func(name string, labels map[string]string) *kube.KusoAddon {
		return &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
	}

	cases := []struct {
		name string
		a    *kube.KusoAddon
		want bool
	}{
		{
			// The regression: a genuine project addon that merely shares
			// the "-pr-2" name shape and carries no preview/env labels.
			"real addon named events-pr-2 is NOT a clone",
			addon("myproj-events-pr-2", nil),
			false,
		},
		{
			"labeled clone (preview-pr matches) IS a clone",
			addon("myproj-pg-pr-2", map[string]string{
				previewPRLabel: "2",
				kube.LabelEnv:  "preview-pr-2",
			}),
			true,
		},
		{
			"clone for a DIFFERENT PR is not swept for PR 2",
			addon("myproj-pg-pr-9", map[string]string{
				previewPRLabel: "9",
				kube.LabelEnv:  "preview-pr-9",
			}),
			false,
		},
		{
			"legacy clone: no preview-pr label but has env label + suffix",
			addon("myproj-pg-pr-2", map[string]string{
				kube.LabelEnv: "preview-pr-2",
			}),
			true,
		},
		{
			"legacy fallback does NOT fire without the env label",
			addon("myproj-events-pr-2", map[string]string{
				"kuso.sislelabs.com/project": "myproj",
			}),
			false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPRClone(tc.a, prLabel, suffix); got != tc.want {
				t.Errorf("isPRClone(%q, labels=%v) = %v, want %v", tc.a.Name, tc.a.Labels, got, tc.want)
			}
		})
	}
}

// findEnv returns the EnvVar with the given name, or nil.
func findEnv(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

// TestBuildSeedJob_SourcesHostFromConnSecret locks in the v0.17.27 fix
// for the bug that left 27 orphaned Failed seed Jobs on a closed PR.
//
// Two regressions are guarded here:
//  1. HOST/USER/DB came from string-concatenation ("<fqn>-postgresql")
//     and literals ("kuso"). The "-postgresql" suffix doesn't resolve
//     in-cluster (the Service name is just the addon CR name), so every
//     seed failed with "could not translate host name". All of
//     HOST/USER/DB must now be read from the addon's -conn Secret.
//  2. The Job had no ownerReference and no TTL, so a Failed Job was
//     never GC'd. It must now be owned by the clone addon CR (cascade on
//     PR-close) and carry TTLSecondsAfterFinished (reap stale resyncs).
func TestBuildSeedJob_SourcesHostFromConnSecret(t *testing.T) {
	t.Parallel()

	const (
		ns        = "kuso"
		project   = "tickero"
		sourceFQN = "tickero-db"
		cloneFQN  = "tickero-db-pr-35"
	)
	job := buildSeedJob(ns, project, sourceFQN, cloneFQN, "owner-uid-123", 1780059297)

	env := job.Spec.Template.Spec.Containers[0].Env

	// Every HOST/USER/DB/PASSWORD var must come from a secretKeyRef
	// against the matching -conn Secret — never a literal Value.
	wantRefs := map[string]struct{ secret, key string }{
		"SRC_HOST":     {"tickero-db-conn", "POSTGRES_HOST"},
		"SRC_USER":     {"tickero-db-conn", "POSTGRES_USER"},
		"SRC_DB":       {"tickero-db-conn", "POSTGRES_DB"},
		"SRC_PASSWORD": {"tickero-db-conn", "POSTGRES_PASSWORD"},
		"DST_HOST":     {"tickero-db-pr-35-conn", "POSTGRES_HOST"},
		"DST_USER":     {"tickero-db-pr-35-conn", "POSTGRES_USER"},
		"DST_DB":       {"tickero-db-pr-35-conn", "POSTGRES_DB"},
		"DST_PASSWORD": {"tickero-db-pr-35-conn", "POSTGRES_PASSWORD"},
	}
	for name, want := range wantRefs {
		e := findEnv(env, name)
		if e == nil {
			t.Errorf("%s: missing env var", name)
			continue
		}
		if e.Value != "" {
			t.Errorf("%s: has literal Value %q; must be a secretKeyRef (the old '-postgresql' host bug)", name, e.Value)
			continue
		}
		ref := e.ValueFrom.SecretKeyRef
		if ref.Name != want.secret || ref.Key != want.key {
			t.Errorf("%s: secretKeyRef = %s/%s, want %s/%s", name, ref.Name, ref.Key, want.secret, want.key)
		}
	}

	// No env var may carry the dead "-postgresql" host suffix anywhere.
	for _, e := range env {
		if e.Value == sourceFQN+"-postgresql" || e.Value == cloneFQN+"-postgresql" {
			t.Errorf("%s still uses the broken '-postgresql' host suffix: %q", e.Name, e.Value)
		}
	}
}

func TestBuildSeedJob_OwnerRefAndTTL(t *testing.T) {
	t.Parallel()

	job := buildSeedJob("kuso", "tickero", "tickero-db", "tickero-db-pr-35", "owner-uid-123", 1780059297)

	// TTL must be set so Failed Jobs self-reap.
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Fatal("TTLSecondsAfterFinished is nil; Failed seed Jobs would orphan forever")
	}
	if got := *job.Spec.TTLSecondsAfterFinished; got != 3600 {
		t.Errorf("TTLSecondsAfterFinished = %d, want 3600", got)
	}

	// Owner ref must point at the clone addon CR so kube-GC cascades the
	// Job delete when DeletePRAddons drops the clone on PR-close.
	if len(job.OwnerReferences) != 1 {
		t.Fatalf("OwnerReferences = %d, want 1 (clone addon CR)", len(job.OwnerReferences))
	}
	o := job.OwnerReferences[0]
	if o.Kind != "KusoAddon" || o.Name != "tickero-db-pr-35" || o.UID != "owner-uid-123" {
		t.Errorf("owner ref = %s/%s uid=%s, want KusoAddon/tickero-db-pr-35 uid=owner-uid-123", o.Kind, o.Name, o.UID)
	}
	// Controller=false + BlockOwnerDeletion=false: must not deadlock the
	// clone addon's helm-uninstall finalizer behind Job GC.
	if o.Controller == nil || *o.Controller {
		t.Error("owner ref Controller must be explicitly false")
	}
	if o.BlockOwnerDeletion == nil || *o.BlockOwnerDeletion {
		t.Error("owner ref BlockOwnerDeletion must be explicitly false")
	}
}

func TestBuildSeedJob_NoOwnerWhenUIDEmpty(t *testing.T) {
	t.Parallel()

	// Best-effort: if the clone CR lookup failed, the Job is still
	// created (TTL is the fallback reaper) but without an owner ref.
	job := buildSeedJob("kuso", "tickero", "tickero-db", "tickero-db-pr-35", "", 1780059297)
	if len(job.OwnerReferences) != 0 {
		t.Errorf("OwnerReferences = %d, want 0 when UID is empty", len(job.OwnerReferences))
	}
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Error("TTL must still be set even without an owner ref")
	}
}

// TestIsPreviewCloneName locks in the addon-clone idempotency fix
// from v0.17.6. EnsurePRAddons used to call Addons.List then clone
// every postgres addon it saw — including addons that were
// themselves clones from a previous PR sync — producing names like
// "tickero-pg-pr-35-pr-35-pr-35-pr-35". This regex is the filter
// that breaks that loop.
func TestIsPreviewCloneName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"source addon", "tickero-pg", false},
		{"source with hyphens", "tickero-prod-db", false},
		{"normal clone", "tickero-pg-pr-35", true},
		{"normal clone single-segment source", "pg-pr-42", true},
		{"double-cloned (the bug case)", "tickero-pg-pr-35-pr-35", true},
		{"triple-cloned", "tickero-pg-pr-35-pr-35-pr-35", true},
		{"different PR numbers (still a clone)", "tickero-pg-pr-1-pr-2", true},
		// Edge cases that look like clones but aren't.
		{"pr in middle of name (not suffix)", "tickero-pr-team-db", false},
		{"pr suffix without number", "tickero-pg-pr", false},
		{"non-numeric suffix", "tickero-pg-pr-abc", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isPreviewCloneName(tc.in)
			if got != tc.want {
				t.Errorf("isPreviewCloneName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
