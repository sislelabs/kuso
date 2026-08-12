package handlers

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Guards the write-path credential leak: the repo-URL redaction
// (redact*RepoIfNeeded) originally landed only on the GET/List/Describe
// handlers, while every MUTATING handler (Update, PatchService, AddDomain,
// SetEnvVar, …) echoed the full CR — so an editor without secrets:read
// saved a port and got the deploy-token URL back in the response body.
// The round-trip preservation logic guarantees the credential is present
// in the CR those handlers serialize, which made the leak deterministic.
//
// These are structural source scans (same pattern as the CLI's
// output_format_test.go and projects' env_literals_test.go): they pin
// the "every serialization site routes through the redact helpers"
// contract stated on redactProjectRepoIfNeeded, because a forgotten call
// in a future handler compiles clean and leaks silently.

// handlerBodies returns ProjectsHandler method name → body source.
func handlerBodies(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile("projects.go")
	if err != nil {
		t.Fatalf("read projects.go: %v", err)
	}
	parts := regexp.MustCompile(`(?m)^func `).Split(string(src), -1)
	nameRe := regexp.MustCompile(`^\(h \*ProjectsHandler\) (\w+)\(`)
	out := map[string]string{}
	for _, p := range parts {
		if m := nameRe.FindStringSubmatch(p); m != nil {
			out[m[1]] = p
		}
	}
	return out
}

// Handlers that serialize a *kube.KusoService must redact its repo ref.
func TestServiceEchoingHandlersRedactRepo(t *testing.T) {
	t.Parallel()
	bodies := handlerBodies(t)
	must := []string{
		"AddService", "GetService", "PatchService", "RenameService",
		"AddDomain", "RemoveDomain", "SetEnvVar", "UnsetEnvVar",
	}
	for _, name := range must {
		body, ok := bodies[name]
		if !ok {
			t.Errorf("handler %s not found in projects.go — update this test's list", name)
			continue
		}
		if !strings.Contains(body, "redactServiceRepoIfNeeded(") {
			t.Errorf("%s serializes a KusoService but never calls redactServiceRepoIfNeeded — "+
				"its response echoes the deploy-token repo URL to editors without secrets:read", name)
		}
	}
}

// Handlers that serialize a *kube.KusoProject must redact defaultRepo.
func TestProjectEchoingHandlersRedactRepo(t *testing.T) {
	t.Parallel()
	bodies := handlerBodies(t)
	for _, name := range []string{"List", "Create", "Describe", "Update"} {
		body, ok := bodies[name]
		if !ok {
			t.Errorf("handler %s not found in projects.go — update this test's list", name)
			continue
		}
		if !strings.Contains(body, "redactProjectRepoIfNeeded(") {
			t.Errorf("%s serializes a KusoProject but never calls redactProjectRepoIfNeeded — "+
				"its response echoes the deploy-token defaultRepo URL to callers without secrets:read", name)
		}
	}
}

// Belt-and-suspenders: the mask/redact pairing. Any handler that masks
// env values is by definition serializing service CR(s) to a possibly
// unprivileged caller, so it must also redact the repo ref(s). Catches
// the NEXT service-echoing handler someone adds with mask but not
// redact — in BOTH the single-CR and slice forms (the plural helpers
// are not substrings of the singular ones, so each pair is checked
// explicitly).
func TestEveryMaskedServiceEchoAlsoRedacts(t *testing.T) {
	t.Parallel()
	pairs := []struct{ mask, redact string }{
		{"maskServiceEnvIfNeeded(", "redactServiceRepoIfNeeded("},
		{"maskServicesEnvIfNeeded(", "redactServicesRepoIfNeeded("},
	}
	for name, body := range handlerBodies(t) {
		for _, p := range pairs {
			if strings.Contains(body, p.mask) && !strings.Contains(body, p.redact) {
				t.Errorf("%s calls %s but not %s — "+
					"the repo deploy-token leaks on the same response the env mask protects",
					name, strings.TrimSuffix(p.mask, "("), strings.TrimSuffix(p.redact, "("))
			}
		}
	}
}
