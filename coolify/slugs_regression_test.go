package coolify

import (
	"regexp"
	"testing"
)

func TestAssignKusoSlugs_NeverHandsOutSameSlugTwice(t *testing.T) {
	names := []string{"acme", "acme-2", "Acme!", "ACME"}
	out := AssignKusoSlugs(names)
	seen := map[string]string{}
	for _, n := range names {
		s := out[n]
		if prev, dup := seen[s]; dup {
			t.Fatalf("%q and %q both got slug %q (all: %v)", prev, n, s, out)
		}
		seen[s] = n
	}
	if out["acme"] != "acme" || out["acme-2"] != "acme-2" {
		t.Errorf("first-come names should keep their bare slug: %v", out)
	}
}

// Mirrors server-go serviceNameRE and validateProjectName.
var (
	serverServiceName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
	serverProjectName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$`)
)

func TestSlugsFitServerNameLimits(t *testing.T) {
	app := &Application{GitRepository: "acme/customer-portal-frontend-service-nextjs-app"}
	taken := map[string]bool{}
	for i := 0; i < 3; i++ {
		s := UniqueSlug(ServiceSlugFromApp(app), taken)
		if !serverServiceName.MatchString(s) {
			t.Errorf("service slug %q (%d chars) fails serviceNameRE", s, len(s))
		}
	}
	long := "2024 Customer Portal Frontend Service Next.js Monorepo"
	for name, s := range AssignKusoSlugs([]string{long, long + "!", long + "?"}) {
		if !serverProjectName.MatchString(s) {
			t.Errorf("project %q → slug %q (%d chars) fails validateProjectName", name, s, len(s))
		}
	}
}
