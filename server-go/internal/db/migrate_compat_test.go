package db

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// breakingMigrationMarker opts a migration out of the compat check. Only
// use it with a plan for the previous binary: migrations run at boot
// during a maxSurge:1 roll (the old pod keeps serving against the new
// schema) and the updater may revert to the prior server image.
const breakingMigrationMarker = "-- kuso:breaks-previous-release:"

var breakingDDL = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?is)\bDROP\s+COLUMN\b`), "DROP COLUMN"},
	{regexp.MustCompile(`(?is)\bRENAME\s+COLUMN\b`), "RENAME COLUMN"},
	{regexp.MustCompile(`(?is)\bALTER\s+TABLE\b[^;]*\bRENAME\s+TO\b`), "table RENAME"},
	{regexp.MustCompile(`(?is)\bDROP\s+TABLE\b`), "DROP TABLE"},
	{regexp.MustCompile(`(?is)\bALTER\s+COLUMN\b[^;]*\bTYPE\b`), "ALTER COLUMN ... TYPE"},
	{regexp.MustCompile(`(?is)\bALTER\s+COLUMN\b[^;]*\bSET\s+NOT\s+NULL\b`), "SET NOT NULL"},
}

var addColumn = regexp.MustCompile(`(?is)\bADD\s+COLUMN\b[^,;]*`)

// backwardIncompatible lists statements in sql that the previous server
// binary can't run against: dropped/renamed/retyped columns and tables,
// and new NOT NULL constraints (incl. a NOT NULL column without DEFAULT,
// which breaks the old binary's INSERTs).
func backwardIncompatible(sql string) []string {
	var out []string
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	body := b.String()
	for _, d := range breakingDDL {
		if d.re.MatchString(body) {
			out = append(out, d.what)
		}
	}
	for _, m := range addColumn.FindAllString(body, -1) {
		u := strings.ToUpper(m)
		if strings.Contains(u, "NOT NULL") && !strings.Contains(u, "DEFAULT") {
			out = append(out, "ADD COLUMN ... NOT NULL without DEFAULT")
		}
	}
	return out
}

// Migrations must stay compatible with the previous release's binary,
// the way hack/crd-guard.py keeps CRDs additive.
func TestMigrations_BackwardCompatible(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrationsFS, f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), breakingMigrationMarker) {
			continue
		}
		if bad := backwardIncompatible(string(raw)); len(bad) > 0 {
			t.Errorf("%s breaks the previous server binary (%s). Split it into an additive step now and the "+
				"destructive step a release later, or mark it %q with the plan", f, strings.Join(bad, ", "), breakingMigrationMarker+" <why>")
		}
	}
}

func TestBackwardIncompatible_Detects(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		`ALTER TABLE "User" DROP COLUMN "x";`:            "DROP COLUMN",
		`ALTER TABLE "User" RENAME COLUMN "a" TO "b";`:   "RENAME COLUMN",
		`ALTER TABLE "User" RENAME TO "Person";`:         "table RENAME",
		`DROP TABLE IF EXISTS "Old";`:                    "DROP TABLE",
		`ALTER TABLE "T" ALTER COLUMN "c" TYPE bigint;`:  "ALTER COLUMN ... TYPE",
		`ALTER TABLE "T" ALTER COLUMN "c" SET NOT NULL;`: "SET NOT NULL",
		`ALTER TABLE "T" ADD COLUMN "n" text NOT NULL;`:  "ADD COLUMN ... NOT NULL without DEFAULT",
		"ALTER TABLE \"T\"\n  DROP COLUMN\n  \"x\";":     "DROP COLUMN",
	}
	for sql, want := range bad {
		if got := backwardIncompatible(sql); len(got) != 1 || got[0] != want {
			t.Errorf("%q: got %v, want [%s]", sql, got, want)
		}
	}
	ok := []string{
		`ALTER TABLE "T" ADD COLUMN "n" text NOT NULL DEFAULT '';`,
		`ALTER TABLE "T" ADD COLUMN IF NOT EXISTS "n" text;`,
		`ALTER TABLE "Audit" DROP CONSTRAINT IF EXISTS "Audit_user_fkey";`,
		`CREATE INDEX IF NOT EXISTS "i" ON "T" ("c");`,
		`-- we used to DROP COLUMN here` + "\nSELECT 1;",
		`ALTER TABLE "T" ALTER COLUMN "c" DROP NOT NULL;`,
	}
	for _, sql := range ok {
		if got := backwardIncompatible(sql); len(got) != 0 {
			t.Errorf("%q flagged %v, want none", sql, got)
		}
	}
}
