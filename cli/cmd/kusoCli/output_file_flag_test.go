package kusoCli

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// On ~59 commands `-o/--output` selects the output FORMAT (json/table).
// `kuso backup` and `kuso addon-backup download` historically bound the
// same flag to the destination FILE, so `kuso backup -o json` silently
// wrote the dump to a file literally named "json". The file meaning
// moved to a long-only --file flag; -o/--output on these two commands
// is now rejected with an error that points at --file, whatever value
// it carries. These tests pin that contract.

// resetFlag clears a string flag's value AND its Changed bit so one
// test's Set() doesn't leak into the next.
func resetFlag(t *testing.T, cmdFlags interface {
	Set(string, string) error
}, name string) {
	t.Helper()
	// pflag has no public "unchange"; tests below each re-Set the flag
	// they need, and every assertion is on the error path where the
	// Changed bit is exactly what we want to keep asserted. Setting back
	// to "" keeps values from leaking.
	_ = cmdFlags.Set(name, "")
}

func TestBackupDashOValueRefusedWithFileHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
		val  string
	}{
		// historically `kuso backup -o json` wrote a file named "json"
		{"BackupOutputJSONNoLongerCreatesFile", backupCmd, nil, "json"},
		// -o <path> must error too: the file meaning lives on --file only
		{"BackupOutputPathValueErrorsWithFileHint", backupCmd, nil, "dump.sql.gz"},
		{"AddonBackupDownloadOutputJSONNoLongerCreatesFile", addonBackupDownloadCmd, []string{"proj", "db"}, "json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := tc.cmd.Flags().Set("output", tc.val); err != nil {
				t.Fatalf("set -o %s: %v", tc.val, err)
			}
			t.Cleanup(func() { resetFlag(t, tc.cmd.Flags(), "output") })

			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil {
				t.Fatalf("%s -o %s did not error", tc.cmd.CommandPath(), tc.val)
			}
			if !strings.Contains(err.Error(), "--file") {
				t.Errorf("error should point the user at --file, got: %v", err)
			}
			if _, statErr := os.Stat(tc.val); statErr == nil {
				t.Errorf("a file literally named %q was created — the exact bug this sweep fixes", tc.val)
			}
		})
	}
}

// The replacement flag: --file, long-only (no shorthand — -f would
// collide with --follow conventions elsewhere and -o is the trap we
// are removing).
func TestBackupCommandsExposeLongOnlyFileFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		find func() bool
		sh   func() string
	}{
		{"backup", func() bool { return backupCmd.Flags().Lookup("file") != nil },
			func() string { return backupCmd.Flags().Lookup("file").Shorthand }},
		{"addon-backup download", func() bool { return addonBackupDownloadCmd.Flags().Lookup("file") != nil },
			func() string { return addonBackupDownloadCmd.Flags().Lookup("file").Shorthand }},
	} {
		if !tc.find() {
			t.Errorf("%s: no --file flag registered", tc.name)
			continue
		}
		if got := tc.sh(); got != "" {
			t.Errorf("%s: --file should be long-only, has shorthand %q", tc.name, got)
		}
	}
}

// `kuso project export distill -o json` wrote the project's kuso.yaml
// to ./json (found live in the 2026-10-01 review).
func TestExportDashOFormatValueRefused(t *testing.T) {
	if _, err := resolveLegacyOutFile("", "json", "--out"); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("-o json must be refused pointing at --out, got %v", err)
	}
	got, err := resolveLegacyOutFile("", "kuso.yaml", "--out")
	if err != nil || got != "kuso.yaml" {
		t.Fatalf("-o <path> should still work as a deprecated alias: %q %v", got, err)
	}
	for _, c := range []*cobra.Command{projectExportCmd, projectExportArchiveCmd} {
		if f := c.Flags().Lookup("out"); f == nil || f.Shorthand != "" {
			t.Errorf("%s: --out must be long-only", c.Name())
		}
		if f := c.Flags().ShorthandLookup("o"); f == nil || !f.Hidden {
			t.Errorf("%s: -o should remain only as a hidden legacy alias", c.Name())
		}
	}
	importOut, importFile = "json", ""
	t.Cleanup(func() { importOut = "" })
	if _, err := importDestFile(); err == nil {
		t.Error("import compose -o json must be refused")
	}
}
