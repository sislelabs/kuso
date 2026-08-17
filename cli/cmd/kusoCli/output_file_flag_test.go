package kusoCli

import (
	"os"
	"strings"
	"testing"
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

func TestBackupOutputJSONNoLongerCreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := backupCmd.Flags().Set("output", "json"); err != nil {
		t.Fatalf("set -o json: %v", err)
	}
	t.Cleanup(func() { resetFlag(t, backupCmd.Flags(), "output") })

	err := backupCmd.RunE(backupCmd, nil)
	if err == nil {
		t.Fatal("kuso backup -o json did not error; historically this wrote a file named \"json\"")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error should point the user at --file, got: %v", err)
	}
	if _, statErr := os.Stat("json"); statErr == nil {
		t.Error("a file literally named \"json\" was created — the exact bug this sweep fixes")
	}
}

func TestBackupOutputPathValueErrorsWithFileHint(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := backupCmd.Flags().Set("output", "dump.sql.gz"); err != nil {
		t.Fatalf("set -o: %v", err)
	}
	t.Cleanup(func() { resetFlag(t, backupCmd.Flags(), "output") })

	err := backupCmd.RunE(backupCmd, nil)
	if err == nil {
		t.Fatal("kuso backup -o <path> should error and direct the user to --file")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error should mention --file, got: %v", err)
	}
	if _, statErr := os.Stat("dump.sql.gz"); statErr == nil {
		t.Error("-o <path> still wrote a file; the file meaning must live on --file only")
	}
}

func TestAddonBackupDownloadOutputJSONNoLongerCreatesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := addonBackupDownloadCmd.Flags().Set("output", "json"); err != nil {
		t.Fatalf("set -o json: %v", err)
	}
	t.Cleanup(func() { resetFlag(t, addonBackupDownloadCmd.Flags(), "output") })

	err := addonBackupDownloadCmd.RunE(addonBackupDownloadCmd, []string{"proj", "db"})
	if err == nil {
		t.Fatal("addon-backup download -o json did not error")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Errorf("error should point the user at --file, got: %v", err)
	}
	if _, statErr := os.Stat("json"); statErr == nil {
		t.Error("a file literally named \"json\" was created")
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
