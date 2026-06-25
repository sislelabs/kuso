package notify

import "testing"

// TestAddonCrashed asserts the addon-crash event carries its own type +
// addon identity and does NOT masquerade as a service crash. Before this,
// crashing addon pods went through PodCrashed, which read the addon FQN as
// a "service" and deep-linked to a non-existent service overlay.
func TestAddonCrashed(t *testing.T) {
	e := AddonCrashed("proj", "db", "postgres", "proj-db-0", "CrashLoopBackOff", "fatal: ...", 4)

	if e.Type != EventAddonCrashed {
		t.Errorf("Type = %q, want %q", e.Type, EventAddonCrashed)
	}
	// Must NOT set Service (that's what produced the phantom-service link).
	if e.Service != "" {
		t.Errorf("addon crash must not set Service, got %q", e.Service)
	}
	if e.Project != "proj" {
		t.Errorf("Project = %q, want proj", e.Project)
	}
	if e.Extra["addon"] != "db" || e.Extra["addonKind"] != "postgres" {
		t.Errorf("Extra addon identity wrong: %+v", e.Extra)
	}
	// Higher severity than a single service pod — a datastore crash takes
	// down every consumer in the project.
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want error", e.Severity)
	}
	// Deep-links to the project (canvas), where the addon node lives —
	// not to a service URL.
	if e.URL == "" {
		t.Errorf("expected a project deep-link URL")
	}
}
