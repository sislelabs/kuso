package kusoCli

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/sislelabs/kuso/coolify"
)

// The generated script must hand bash the source URL byte-for-byte: a
// password with $(…), backticks, $HOME or a newline must neither execute
// nor be expanded, and must not escape a comment line.
func TestBuildDataMigrationScript_SourceURLIsInert(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	url := "postgres://u:p$(echo INJECTED)`id`$HOME'q\"\nx@db.example.com:5432/app"
	it := coolify.Item{ProjectName: "shop", Database: &coolify.Database{
		Name: "db", DatabaseType: "standalone-postgresql",
		InternalDBURL: "postgres://u:p@internal-docker-host:5432/app",
		ExternalDBURL: url,
	}}
	script := buildDataMigrationScript(it)

	i := strings.Index(script, "\nDST=")
	if i < 0 {
		t.Fatalf("no DST= line in script:\n%s", script)
	}
	probe := script[:i] + "\nprintf '%s' \"$SRC\"\n"
	out, err := exec.Command("bash", "-c", probe).Output()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, probe)
	}
	if string(out) != url {
		t.Fatalf("SRC mangled by bash:\n got %q\nwant %q", out, url)
	}
	if strings.Contains(script, "internal-docker-host") {
		t.Errorf("script uses the docker-internal URL although external_db_url is set")
	}
}

func TestBuildDataMigrationScript_FallsBackToInternalURL(t *testing.T) {
	it := coolify.Item{Database: &coolify.Database{
		Name: "db", DatabaseType: "standalone-postgresql",
		InternalDBURL: "postgres://u:p@internal-docker-host:5432/app",
	}}
	if s := buildDataMigrationScript(it); !strings.Contains(s, "SRC='postgres://u:p@internal-docker-host:5432/app'") {
		t.Errorf("expected single-quoted internal URL fallback:\n%s", s)
	}
}
