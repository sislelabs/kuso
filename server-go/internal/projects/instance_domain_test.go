package projects

import "testing"

// The default-base-domain fallback (used when a project sets no
// spec.baseDomain) must come from KUSO_DOMAIN, not a hardcoded literal,
// so a cluster installed on its own domain serves apps there. These
// pin that defaultHost/buildEnvHost resolve the instance domain when no
// per-project base is given, and still honour an explicit base domain
// unchanged. t.Setenv forbids t.Parallel.

func TestDefaultHost_UsesInstanceDomain(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "apps.example.com")

	// service == project → "<project>.<instance-domain>"
	if got := defaultHost("web", "web", ""); got != "web.apps.example.com" {
		t.Errorf("service==project: got %q, want web.apps.example.com", got)
	}
	// service != project → "<service>.<project>.<instance-domain>"
	if got := defaultHost("api", "alpha", ""); got != "api.alpha.apps.example.com" {
		t.Errorf("service!=project: got %q, want api.alpha.apps.example.com", got)
	}
	// An explicit per-project base domain is used verbatim (not the
	// instance default) — the user owns that eTLD+1.
	if got := defaultHost("api", "alpha", "tickero.bg"); got != "api.tickero.bg" {
		t.Errorf("explicit base: got %q, want api.tickero.bg", got)
	}
}

func TestBuildEnvHost_UsesInstanceDomain(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "apps.example.com")

	// No base domain → instance default, project-scoped.
	got := buildEnvHost("", "alpha", "web", "staging")
	if got != "web-staging.alpha.apps.example.com" {
		t.Errorf("empty base: got %q, want web-staging.alpha.apps.example.com", got)
	}
}
