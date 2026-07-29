package addons

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// BrowserRole is the dedicated NOSUPERUSER login role the SQL browser
// connects as, instead of the addon's admin user (which the stock
// postgres image makes a superuser). A superuser can run
// `COPY … TO PROGRAM` (shell exec inside the pod) and pg_read_file
// regardless of any query denylist; this role cannot, because it lacks
// pg_execute_server_program / pg_read_server_files membership.
const BrowserRole = "kuso_browser"

// browserRoleProvisioned memoizes which addon releases already have the
// browser role so we don't re-run the DDL on every browser request.
// Keyed by "<ns>/<release>". A miss just re-runs the idempotent DDL.
var (
	browserRoleProvisioned   = map[string]struct{}{}
	browserRoleProvisionedMu sync.Mutex
)

// EnsureBrowserRole provisions the NOSUPERUSER kuso_browser login role in
// the addon's postgres, idempotently, via pod-exec as the admin user over
// the local trust socket (same mechanism as RepairPassword). It grants
// read+DML on the public schema and sets default privileges so future app
// tables (created by the admin user) are readable too. Safe to re-run.
// Covers both new and already-deployed addons — no separate migration.
//
// adminUser/pass/dbName come from the addon's -conn Secret; releaseName is
// the addon CR name (StatefulSet pod is <release>-0).
func (s *Service) EnsureBrowserRole(ctx context.Context, ns, releaseName, adminUser, pass, dbName string) error {
	if adminUser == "" || dbName == "" {
		return fmt.Errorf("browser role: missing admin user or db name")
	}
	key := ns + "/" + releaseName
	browserRoleProvisionedMu.Lock()
	_, done := browserRoleProvisioned[key]
	browserRoleProvisionedMu.Unlock()
	if done {
		return nil
	}

	// Passwords are generated alnum/hex, but escape defensively (mirrors
	// RepairPassword). dbName/adminUser are Postgres identifiers (validated
	// upstream); they interpolate as quoted identifiers / role names.
	escPass := strings.ReplaceAll(pass, "'", "''")
	ddl := fmt.Sprintf(`
DO $kuso$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='%[1]s') THEN
    ALTER ROLE %[1]s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '%[2]s';
  ELSE
    CREATE ROLE %[1]s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '%[2]s';
  END IF;
END
$kuso$;
GRANT CONNECT ON DATABASE "%[3]s" TO %[1]s;
GRANT USAGE ON SCHEMA public TO %[1]s;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
GRANT SELECT, USAGE ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
ALTER DEFAULT PRIVILEGES FOR ROLE %[4]s IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[1]s;
ALTER DEFAULT PRIVILEGES FOR ROLE %[4]s IN SCHEMA public GRANT SELECT, USAGE ON SEQUENCES TO %[1]s;
`, BrowserRole, escPass, dbName, adminUser)

	podName := releaseName + "-0"
	// psql over the local trust socket as the admin OS user — no password
	// needed (upstream postgres image enables `local trust` for the
	// postgres OS user). Try -h /var/run/postgresql first, then the plain
	// form, matching RepairPassword's fallback.
	argv := func(hostFlag bool) []string {
		a := []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", adminUser, "-d", dbName}
		if hostFlag {
			a = append(a, "-h", "/var/run/postgresql")
		}
		return append(a, "-c", ddl)
	}
	_, stderr, err := s.execInPod(ctx, ns, podName, "postgres", argv(true))
	if err != nil {
		_, stderr2, err2 := s.execInPod(ctx, ns, podName, "postgres", argv(false))
		if err2 != nil {
			return fmt.Errorf("provision browser role: %v: %s (fallback: %v: %s)", err, stderr, err2, stderr2)
		}
	}
	browserRoleProvisionedMu.Lock()
	browserRoleProvisioned[key] = struct{}{}
	browserRoleProvisionedMu.Unlock()
	return nil
}
