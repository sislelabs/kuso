package reconcilehealth

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A <addon>-conn Secret with no KusoAddon behind it is the fingerprint
// of a leaked addon: the CR is gone, but the credential (and, for an
// instance-pg addon, the logical database it names) is still there.
//
// This is how 16 databases hid on the production cluster. Each still
// had a live-looking *-conn Secret, so a "does anything reference it?"
// check said yes, while nothing actually consumed them. Detection has
// to key off the ABSENCE of the owning CR, not the presence of a
// reference.
func TestDetectOrphanConnSecrets(t *testing.T) {
	secrets := []corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{
			Name: "bukvite-db-conn", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "bukvite"},
		}},
		{ObjectMeta: metav1.ObjectMeta{
			Name: "bukvite-db-staging-conn", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "bukvite"},
		}},
		// Not a conn secret at all — must be ignored.
		{ObjectMeta: metav1.ObjectMeta{Name: "bukvite-bukvite30-secrets", Namespace: "kuso"}},
	}
	// Only the production addon still exists.
	live := map[string]bool{"bukvite-db": true}

	got := detectOrphanConnSecrets(secrets, live)

	if len(got) != 1 {
		t.Fatalf("found %d orphans, want 1: %+v", len(got), got)
	}
	if got[0].Resource != "bukvite-db-staging-conn" {
		t.Errorf("Resource = %q, want bukvite-db-staging-conn", got[0].Resource)
	}
	if got[0].Project != "bukvite" {
		t.Errorf("Project = %q, want bukvite", got[0].Project)
	}
	if got[0].Kind != KindOrphanConnSecret {
		t.Errorf("Kind = %q, want %q", got[0].Kind, KindOrphanConnSecret)
	}
	// Must never auto-remediate: dropping a credential could orphan a
	// database an operator still wants to recover.
	if got[0].Safe {
		t.Error("orphan cleanup must not be marked Safe for unattended auto-remediation")
	}
	if got[0].RunbookCmd == "" {
		t.Error("an operator needs a concrete command to act on this")
	}
}

// A live addon's conn secret must never be reported.
func TestDetectOrphanConnSecrets_IgnoresLiveAddons(t *testing.T) {
	secrets := []corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "tickero-psdb-conn", Namespace: "kuso"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "koreni-db-conn", Namespace: "kuso-koreni"}},
	}
	live := map[string]bool{"tickero-psdb": true, "koreni-db": true}

	if got := detectOrphanConnSecrets(secrets, live); len(got) != 0 {
		t.Fatalf("reported %d orphans for live addons: %+v", len(got), got)
	}
}
