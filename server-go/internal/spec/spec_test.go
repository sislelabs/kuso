package spec

import "testing"

func TestParse_FullParityRoundTrips(t *testing.T) {
	raw := []byte(`
apiVersion: kuso/v1
project: shop
baseDomain: shop.example.com
prune: true
services:
  - name: api
    repo: https://github.com/me/api
    branch: main
    runtime: dockerfile
    port: 8080
    internal: false
    privateEgress: true
    domains:
      - host: api.shop.example.com
        tls: true
    env:
      LOG_LEVEL: info
    scale: { min: 2, max: 6, targetCPU: 65 }
    sleep: { enabled: true, afterMinutes: 20 }
    placement:
      labels: { region: eu }
    volumes:
      - { name: data, mountPath: /data, sizeGi: 5 }
addons:
  - name: db
    kind: postgres
    version: "16"
    ha: true
    pooler: { enabled: true }
    backup: { schedule: "0 3 * * *", retentionDays: 7 }
crons:
  - name: nightly
    kind: command
    schedule: "0 2 * * *"
    image: alpine:3
    command: ["sh", "-c", "echo hi"]
`)
	f, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.APIVersion != "kuso/v1" || !f.Prune {
		t.Fatalf("apiVersion/prune not parsed: %+v", f)
	}
	if len(f.Services) != 1 || f.Services[0].Sleep == nil || !f.Services[0].Sleep.Enabled {
		t.Fatalf("service sleep not parsed: %+v", f.Services)
	}
	if f.Services[0].Placement == nil || f.Services[0].Placement.Labels["region"] != "eu" {
		t.Fatalf("placement not parsed: %+v", f.Services[0].Placement)
	}
	if !f.Services[0].PrivateEgress {
		t.Fatalf("privateEgress not parsed")
	}
	if len(f.Addons) != 1 || !f.Addons[0].HA || f.Addons[0].Pooler == nil || !f.Addons[0].Pooler.Enabled {
		t.Fatalf("addon ha/pooler not parsed: %+v", f.Addons)
	}
	if f.Addons[0].Backup == nil || f.Addons[0].Backup.Schedule != "0 3 * * *" {
		t.Fatalf("addon backup not parsed: %+v", f.Addons[0].Backup)
	}
	if len(f.Crons) != 1 || f.Crons[0].Kind != "command" || f.Crons[0].Image != "alpine:3" {
		t.Fatalf("cron not parsed: %+v", f.Crons)
	}
}

func TestParse_SecurityContext(t *testing.T) {
	src := "project: x\nservices:\n  - name: a\n    securityContext:\n" +
		"      capabilities:\n        add: [SETUID, SETGID]\n" +
		"      allowPrivilegeEscalation: true\n"
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sc := f.Services[0].SecurityContext
	if sc == nil {
		t.Fatalf("securityContext not parsed: %+v", f.Services[0])
	}
	if sc.AllowPrivilegeEscalation == nil || !*sc.AllowPrivilegeEscalation {
		t.Fatalf("allowPrivilegeEscalation not parsed: %+v", sc)
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Add) != 2 || sc.Capabilities.Add[0] != "SETUID" || sc.Capabilities.Add[1] != "SETGID" {
		t.Fatalf("capabilities.add not parsed: %+v", sc.Capabilities)
	}
}

func TestParse_RejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte("project: x\nservices:\n  - name: a\n    bogusField: 1\n"))
	if err == nil {
		t.Fatal("expected error for unknown field bogusField")
	}
}

func TestParse_EnvValueUnion(t *testing.T) {
	src := "project: x\nservices:\n  - name: a\n    env:\n" +
		"      LOG_LEVEL: info\n" +
		"      DB: ${{ db.URL }}\n" +
		"      SECRET: { generate: hex32 }\n"
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	env := f.Services[0].Env
	if env["LOG_LEVEL"].Value != "info" || env["LOG_LEVEL"].IsGenerated() {
		t.Fatalf("scalar must be a literal value: %+v", env["LOG_LEVEL"])
	}
	if env["DB"].Value != "${{ db.URL }}" {
		t.Fatalf("varref scalar must be a literal value: %+v", env["DB"])
	}
	if !env["SECRET"].IsGenerated() || env["SECRET"].Generate != "hex32" {
		t.Fatalf("generate mapping must set Generate: %+v", env["SECRET"])
	}
	if env["SECRET"].Value != "" {
		t.Fatalf("generated entry must not carry a value: %+v", env["SECRET"])
	}
}

func TestParse_RejectsUnknownGenerateKind(t *testing.T) {
	_, err := Parse([]byte("project: x\nservices:\n  - name: a\n    env:\n      S: { generate: md5 }\n"))
	if err == nil {
		t.Fatal("expected error for unknown generate kind md5")
	}
}

func TestParse_RejectsUnknownEnvValueField(t *testing.T) {
	_, err := Parse([]byte("project: x\nservices:\n  - name: a\n    env:\n      S: { bogus: 1 }\n"))
	if err == nil {
		t.Fatal("expected error for unknown env-value field bogus")
	}
}

func TestParse_RejectsBadAPIVersion(t *testing.T) {
	_, err := Parse([]byte("apiVersion: kuso/v2\nproject: x\n"))
	if err == nil {
		t.Fatal("expected error for apiVersion kuso/v2")
	}
}

func TestParse_EmptyAPIVersionTolerated(t *testing.T) {
	f, err := Parse([]byte("project: x\n"))
	if err != nil {
		t.Fatalf("empty apiVersion should be tolerated: %v", err)
	}
	if f.Project != "x" {
		t.Fatalf("project not parsed")
	}
}

func TestParse_RejectsBadCronSchedule(t *testing.T) {
	_, err := Parse([]byte("project: x\ncrons:\n  - name: c\n    kind: http\n    schedule: \"not a cron\"\n    url: https://x\n"))
	if err == nil {
		t.Fatal("expected error for bad cron schedule")
	}
}

func TestParse_RejectsExternalAndInstanceAddonConflict(t *testing.T) {
	_, err := Parse([]byte("project: x\naddons:\n  - name: db\n    kind: postgres\n    external: { secretName: s }\n    useInstanceAddon: pg\n"))
	if err == nil {
		t.Fatal("expected error for external + useInstanceAddon on one addon")
	}
}
