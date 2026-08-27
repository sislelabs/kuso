package handlers

import (
	"strings"
	"testing"
)

// TestRedactRunCommand pins the audit-redaction contract for run argv.
//
// Creating a run is admin-only because it is equivalent to a pod shell.
// But its audit row is tagged Pipeline: project, and GET /api/audit
// ?project=<p> serves project-scoped rows to any VIEWER — so an inline
// credential on the command line was handed to strictly less-privileged
// readers. Same inversion audit_redaction_test.go guards on the ssh-key
// and instance-secret surfaces.
func TestRedactRunCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		absent string
	}{
		{"dsn userinfo", `psql postgres://app:hunter2@db/app -c 'select 1'`, "hunter2"},
		{"env assignment", `sh -c "export API_KEY=sk-live-abc123 && ./seed"`, "sk-live-abc123"},
		{"long flag", `./migrate --password=s3cr3t up`, "s3cr3t"},
		{"spaced flag", `node script.js --auth-token abcdef123`, "abcdef123"},
		{"secret flag", `./tool --client-secret=shhh run`, "shhh"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactRunCommand(c.in)
			if strings.Contains(got, c.absent) {
				t.Errorf("credential %q survived redaction\n  in:  %s\n  out: %s", c.absent, c.in, got)
			}
		})
	}

	// Redaction must not destroy the forensic value — recording "who ran
	// DELETE FROM users" is the entire reason runs are audited.
	t.Run("keeps the auditable command", func(t *testing.T) {
		got := redactRunCommand(`psql -c "DELETE FROM users"`)
		if !strings.Contains(got, "DELETE FROM users") {
			t.Errorf("redaction destroyed the auditable command: %s", got)
		}
	})
}
