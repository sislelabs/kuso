package compose

import (
	"strings"
	"testing"
)

// hasNote reports whether any report note for the given service (or ""
// for file-level) contains substr.
func hasNote(rep *Report, service, substr string) bool {
	for _, n := range rep.Notes {
		if n.Service == service && strings.Contains(n.Detail, substr) {
			return true
		}
	}
	return false
}

func hasFlag(rep *Report, service, substr string) bool {
	for _, n := range rep.Notes {
		if n.Service == service && n.Action == ActionFlag && strings.Contains(n.Detail, substr) {
			return true
		}
	}
	return false
}

// TestConvert_ReadOnlyVolumeFlagged asserts a :ro named volume no longer
// silently becomes read-write — the dropped guardrail is flagged.
func TestConvert_ReadOnlyVolumeFlagged(t *testing.T) {
	doc, rep := convertString(t, `
services:
  app:
    image: myorg/app:1
    volumes:
      - config:/etc/app:ro
volumes:
  config:
`)
	// The volume is still imported (kuso can't do ro, but the mount is
	// preserved) — but the read-only inversion must be flagged.
	svc := findService(doc, "app")
	if svc == nil || len(svc.Volumes) != 1 {
		t.Fatalf("expected the volume to still be imported, got: %+v", svc)
	}
	if !hasFlag(rep, "app", "READ-ONLY") {
		t.Errorf(":ro volume should be flagged as a dropped guardrail, got:\n%s", rep.Markdown())
	}
}

// TestConvert_PlatformFlaggedProminently asserts platform is surfaced as
// a FLAG (not a quiet skip) — the arm64/amd64 crashloop trap.
func TestConvert_PlatformFlaggedProminently(t *testing.T) {
	_, rep := convertString(t, `
services:
  app:
    image: myorg/app:1
    platform: linux/arm64
`)
	if !hasFlag(rep, "app", "platform=") {
		t.Errorf("platform should be a FLAG (crashloop risk), got:\n%s", rep.Markdown())
	}
	if !hasNote(rep, "app", "exec format error") {
		t.Errorf("platform flag should mention the exec-format-error crashloop, got:\n%s", rep.Markdown())
	}
}

// TestConvert_NewlyReportedFieldsSurfaced covers the batch of fields that
// were previously dropped with no report output at all.
func TestConvert_NewlyReportedFieldsSurfaced(t *testing.T) {
	_, rep := convertString(t, `
services:
  app:
    image: myorg/app:1
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    read_only: true
    network_mode: host
    pid: host
    dns:
      - 1.1.1.1
    init: true
    expose:
      - "9000"
    container_name: my-app
    devices:
      - /dev/snd:/dev/snd
    tmpfs:
      - /run
    post_start:
      - command: ["echo", "started"]
    pre_stop:
      - command: ["echo", "stopping"]
`)
	wants := []string{
		"cap_drop", "security_opt", "read_only", "network_mode", "pid=",
		"dns", "init", "expose", "container_name", "devices", "tmpfs",
		"post_start", "pre_stop",
	}
	for _, want := range wants {
		if !hasNote(rep, "app", want) {
			t.Errorf("compose key %q should be reported (was silently dropped), got:\n%s", want, rep.Markdown())
		}
	}
	// The security-relevant ones must be FLAGs, not quiet skips.
	for _, want := range []string{"cap_drop", "security_opt", "read_only"} {
		if !hasFlag(rep, "app", want) {
			t.Errorf("security-relevant key %q should be a FLAG, got:\n%s", want, rep.Markdown())
		}
	}
}
