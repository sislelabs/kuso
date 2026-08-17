package handlers

import (
	"os"
	"path/filepath"
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

// allHandlerBodies returns "file.go:MethodName" → body source for EVERY
// handler method in the package, not just the ones in projects.go.
//
// Why this exists: the tests above read projects.go by name, so a
// service-echoing handler living in ANY other file was structurally
// invisible to them. Two real leaks hid in exactly that blind spot —
// shared_env_keys.go and subscribed_addons.go both echoed a
// *kube.KusoService with plaintext spec.envVars to EDITOR-gated callers
// while the guard tests reported green. Scanning the whole package
// closes the blind spot permanently.
func allHandlerBodies(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob handlers: %v", err)
	}
	splitRe := regexp.MustCompile(`(?m)^func `)
	nameRe := regexp.MustCompile(`^\(h \*\w+\) (\w+)\(`)
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, p := range splitRe.Split(string(src), -1) {
			if m := nameRe.FindStringSubmatch(p); m != nil {
				out[f+":"+m[1]] = p
			}
		}
	}
	return out
}

// Any handler ANYWHERE in the package that writes a value produced by a
// Set*/Patch*/Add* service call and named like a service CR must run the
// mask+redact pair. We detect the echo structurally — a writeJSON of a
// variable the handler got back from h.Svc — rather than maintaining a
// hand-written list, because the hand-written list is what failed before.
func TestNoHandlerEchoesServiceWithoutMasking(t *testing.T) {
	t.Parallel()
	// Handlers that legitimately echo a service-shaped payload the
	// caller already supplied, or that serve admin-only routes where
	// the value is intentionally visible, can be listed here WITH a
	// reason. Keep this empty unless there's a real justification.
	allowed := map[string]string{}

	// Detect the echo precisely: the handler must call a service-shaped
	// mutator on h.Svc AND serialize its *kube.KusoService result.
	// Matching on the RESULT TYPE is what keeps this honest — handlers
	// that echo an environment/cron/addon CR carry no plaintext
	// spec.envVars (those live on the service) and must not be flagged.
	svcMutatorRe := regexp.MustCompile(`h\.Svc\.(SetSubscribedAddons|SetSharedEnvKeys|SetEnvVar|UnsetEnvVar|PatchService|AddService|RenameService|AddDomain|RemoveDomain)\(`)
	for name, body := range allHandlerBodies(t) {
		if _, ok := allowed[name]; ok {
			continue
		}
		if !svcMutatorRe.MatchString(body) || !strings.Contains(body, "writeJSON(") {
			continue
		}
		hasMask := strings.Contains(body, "maskServiceEnvIfNeeded(") ||
			strings.Contains(body, "maskServicesEnvIfNeeded(")
		hasRedact := strings.Contains(body, "redactServiceRepoIfNeeded(") ||
			strings.Contains(body, "redactServicesRepoIfNeeded(")
		if !hasMask || !hasRedact {
			missing := ""
			if !hasMask {
				missing += "maskServiceEnvIfNeeded "
			}
			if !hasRedact {
				missing += "redactServiceRepoIfNeeded"
			}
			t.Errorf("%s echoes a KusoService from a mutating Svc call but is missing %s— "+
				"an EDITOR-gated route that returns spec.envVars in plaintext leaks every secret "+
				"on the service to a caller without secrets:read", name, missing)
		}
	}
}
