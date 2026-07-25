package builds

import (
	"strings"
	"testing"
)

// Slashed branch names (deploy/kuso, tenant/acme, flow/some-work) are
// the historic failure mode this file exists to pin down. A slash is
// legal in a git ref but illegal in BOTH a Kubernetes label value and
// a Docker tag, so every point where a ref becomes an identifier has
// to slugify. The original fix covered the CR name, the build-ref
// label and the synthetic-ref path, but missed ImageTag — the build
// then failed at image push instead of at Job creation, which looked
// like a different bug. These tests cover every conversion point so
// the next one added can't silently skip the slugifier.

// dockerTagOK mirrors the registry's tag grammar: [A-Za-z0-9_][A-Za-z0-9_.-]*
// up to 128 chars. We only need the character-class half here.
func dockerTagOK(tag string) bool {
	if tag == "" || len(tag) > 128 {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '.', c == '-':
		default:
			return false
		}
	}
	// A leading separator is not a valid tag start.
	return tag[0] != '.' && tag[0] != '-'
}

// labelValueOK mirrors the k8s label-value grammar: empty, or ≤63 chars
// of alphanumerics/'-'/'_'/'.' starting and ending alphanumeric.
func labelValueOK(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 63 {
		return false
	}
	alnum := func(c byte) bool {
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	}
	if !alnum(v[0]) || !alnum(v[len(v)-1]) {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case alnum(c), c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

var refCases = []string{
	"deploy/kuso",
	"tenant/acme",
	"flow/platform-review-hardening",
	"feature/JIRA-123_some-thing",
	"release/v1.2.3",
	"a/b/c/deeply/nested",
	"main",
	"UPPERCASE/Branch",
	"trailing/slash/",
	"/leading-slash",
	"weird~^:?*[]chars",
	strings.Repeat("very-long-branch-name/", 6),
	"0123456789abcdef0123456789abcdef01234567", // full 40-char SHA
}

func TestImageTagIsAlwaysAValidDockerTag(t *testing.T) {
	for _, ref := range refCases {
		tag := ImageTag(ref)
		if !dockerTagOK(tag) {
			t.Errorf("ImageTag(%q) = %q, which is not a valid Docker tag", ref, tag)
		}
	}
}

func TestShortRefIsAlwaysAValidLabelValue(t *testing.T) {
	for _, ref := range refCases {
		if got := shortRef(ref); !labelValueOK(got) {
			t.Errorf("shortRef(%q) = %q, which is not a valid label value", ref, got)
		}
	}
}

func TestBuildCRNameIsAlwaysAValidResourceName(t *testing.T) {
	// A CR name must be a DNS-1123 subdomain and ≤253 chars; in
	// practice the derived Job name keeps us under 63.
	for _, ref := range refCases {
		name := buildCRName("tickero", "api", ref)
		if len(name) > 63 {
			t.Errorf("buildCRName(tickero, api, %q) = %q (%d chars), exceeds 63", ref, name, len(name))
		}
		if strings.ContainsAny(name, "/~^:?*[]") {
			t.Errorf("buildCRName(tickero, api, %q) = %q, contains an illegal character", ref, name)
		}
		if name != strings.ToLower(name) {
			t.Errorf("buildCRName(tickero, api, %q) = %q, must be lowercase", ref, name)
		}
	}
}

// A full SHA must keep the exact 12-char short form on every path —
// the promote/rollback logic and `git rev-parse --short` comparisons
// depend on it, so the slugifier must not disturb it.
func TestFullSHAKeepsShortForm(t *testing.T) {
	const sha = "3abf9b9927bb4d2d6c5844da49fdb665cb671bae"
	if got := ImageTag(sha); got != "3abf9b9927bb" {
		t.Errorf("ImageTag(sha) = %q, want 3abf9b9927bb", got)
	}
	if got := shortRef(sha); got != "3abf9b9927bb" {
		t.Errorf("shortRef(sha) = %q, want 3abf9b9927bb", got)
	}
}

// The tag and the CR-name suffix must agree for a non-SHA ref.
// dispatcher.stampExistingBuildImage reconstructs a build name using
// ImageTag while buildCRName uses shortRef; if the two ever diverge
// again that reconstruction silently targets a CR that doesn't exist.
func TestImageTagAgreesWithShortRef(t *testing.T) {
	for _, ref := range refCases {
		if ImageTag(ref) != shortRef(ref) {
			t.Errorf("ImageTag(%q) = %q but shortRef = %q; the two ref→identifier paths must agree",
				ref, ImageTag(ref), shortRef(ref))
		}
	}
}
