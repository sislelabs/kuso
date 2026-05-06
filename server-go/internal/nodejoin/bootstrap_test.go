package nodejoin

import (
	"strings"
	"testing"
)

func TestBuildInstallCommand_StableLabelOrder(t *testing.T) {
	// Iterating a map in Go is non-deterministic. We sort label keys
	// internally so the same input always produces the same output —
	// otherwise tests, audit logs, and replay debugging are noisy.
	in := map[string]string{
		"region":        "eu",
		"tier":          "premium",
		"arch":          "amd64",
		"instance-type": "cpx21",
	}
	got1 := BuildInstallCommand("https://kp:6443", "tok", in, "")
	got2 := BuildInstallCommand("https://kp:6443", "tok", in, "")
	if got1 != got2 {
		t.Fatalf("non-deterministic install command:\n%s\nvs\n%s", got1, got2)
	}
}

func TestBuildInstallCommand_LabelsAndNodeName(t *testing.T) {
	cmd := BuildInstallCommand("https://kp:6443", "secret", map[string]string{
		"region": "eu",
		"tier":   "premium",
	}, "worker-1")
	if !strings.Contains(cmd, `K3S_URL='https://kp:6443'`) {
		t.Errorf("missing K3S_URL: %s", cmd)
	}
	if !strings.Contains(cmd, `K3S_TOKEN='secret'`) {
		t.Errorf("missing K3S_TOKEN: %s", cmd)
	}
	// The exec arg lives inside the outer-quoted INSTALL_K3S_EXEC=
	// pair; match the agent-flag form directly.
	exec := BuildAgentExec(map[string]string{"region": "eu", "tier": "premium"}, "worker-1")
	for _, want := range []string{
		`--node-label 'region'='eu'`,
		`--node-label 'tier'='premium'`,
		`--node-name 'worker-1'`,
	} {
		if !strings.Contains(exec, want) {
			t.Errorf("BuildAgentExec missing %q: %s", want, exec)
		}
	}
}

func TestBuildInstallCommand_NoLabels(t *testing.T) {
	exec := BuildAgentExec(nil, "")
	if exec != "agent" {
		t.Errorf("expected bare 'agent' exec arg, got %q", exec)
	}
	cmd := BuildInstallCommand("https://kp:6443", "tok", nil, "")
	if !strings.Contains(cmd, `INSTALL_K3S_EXEC='agent'`) {
		t.Errorf("expected outer-quoted 'agent' exec arg: %s", cmd)
	}
}

func TestBuildInstallCommand_LabelEscaping(t *testing.T) {
	// A malicious label value with a single quote must be escaped so
	// the install pipe doesn't get hijacked. shEscape doubles single
	// quotes via the standard '\''\''' shell idiom — assert against
	// the raw exec arg, not the doubly-quoted full pipe.
	exec := BuildAgentExec(map[string]string{
		"role": "evil'; rm -rf /",
	}, "")
	want := `--node-label 'role'='evil'\''; rm -rf /'`
	if !strings.Contains(exec, want) {
		t.Errorf("expected escaped single-quote: got %s", exec)
	}
	// The closing quote of the value must come AFTER the rm fragment;
	// any orphan unquoted "; rm -rf /" would be a real injection.
	if strings.Contains(exec, `' '; rm`) || strings.Contains(exec, `'; rm -rf /' `) {
		t.Errorf("possible quote-break injection in: %s", exec)
	}
}

func TestBuildOneLiner_TrimsTrailingSlash(t *testing.T) {
	got := BuildOneLiner("https://kuso.example.com/", "abc123")
	want := "curl -fsSL https://kuso.example.com/bootstrap?token=abc123 | sudo sh"
	if got != want {
		t.Errorf("BuildOneLiner: got %q want %q", got, want)
	}
}

func TestRenderScript_RequiresParams(t *testing.T) {
	if _, err := RenderScript(ScriptParams{}); err == nil {
		t.Errorf("expected error for empty params")
	}
	if _, err := RenderScript(ScriptParams{PublicURL: "https://x"}); err == nil {
		t.Errorf("expected error for missing JTI")
	}
	if _, err := RenderScript(ScriptParams{JTI: "x"}); err == nil {
		t.Errorf("expected error for missing PublicURL")
	}
}

func TestRenderScript_BakesParams(t *testing.T) {
	s, err := RenderScript(ScriptParams{
		PublicURL: "https://kuso.example.com",
		JTI:       "abc123",
	})
	if err != nil {
		t.Fatalf("RenderScript: %v", err)
	}
	for _, want := range []string{
		"#!/bin/sh",
		"set -eu",
		"KUSO_URL='https://kuso.example.com'",
		"KUSO_TOKEN='abc123'",
		"/bootstrap/register-node",
		"INSTALL_K3S_EXEC=", // redacted log line still mentions the env var
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	// Critical: the script never logs the k3s shared secret in plain
	// form (we redact it before the install command runs).
	if !strings.Contains(s, "K3S_TOKEN=***") {
		t.Errorf("script must redact k3s token in operator log")
	}
}

func TestGenerateJTI_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		jti, err := GenerateJTI()
		if err != nil {
			t.Fatalf("GenerateJTI: %v", err)
		}
		if seen[jti] {
			t.Errorf("collision at iteration %d: %s", i, jti)
		}
		seen[jti] = true
		if len(jti) < 16 {
			t.Errorf("jti too short: %s", jti)
		}
	}
}

func TestMergeFactLabels_OperatorWins(t *testing.T) {
	got := MergeFactLabels(
		map[string]string{
			"region": "eu-west", // operator override
			"tier":   "premium",
		},
		RegisterRequest{
			Arch:          "arm64",
			CloudProvider: "hetzner",
			InstanceType:  "cax21",
			Region:        "fsn1", // overridden by operator
		},
	)
	if got["region"] != "eu-west" {
		t.Errorf("operator region must win: %s", got["region"])
	}
	if got["arch"] != "arm64" {
		t.Errorf("fact arch dropped: %v", got)
	}
	if got["cloud"] != "hetzner" {
		t.Errorf("fact cloud dropped: %v", got)
	}
	if got["instance-type"] != "cax21" {
		t.Errorf("fact instance-type dropped: %v", got)
	}
	if got["tier"] != "premium" {
		t.Errorf("operator tier missing: %v", got)
	}
}

func TestMergeFactLabels_EmptyFactsDropped(t *testing.T) {
	got := MergeFactLabels(nil, RegisterRequest{Arch: "", CloudProvider: ""})
	if len(got) != 0 {
		t.Errorf("empty facts should produce empty labels: %v", got)
	}
}
