package kusoCli

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// retiredBackupFlag matches doc examples that still pass -o/--output as the
// destination file to `kuso backup` or `kuso addon-backup download`. Both
// commands now reject that flag (see rejectRetiredOutputFlag), so a copied
// example — e.g. a nightly cron line — fails and no backup gets taken.
var retiredBackupFlag = regexp.MustCompile(`(^|[\s\x60])(kuso )?backup (-o|--output)\b|addon-backup download[^\n]*[\s\[](-o|--output)\b`)

func TestDocsDoNotUseRetiredBackupOutputFlag(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	targets := []string{"README.md", "docs", "skills"}

	var hits []string
	for _, target := range targets {
		base := filepath.Join(root, target)
		if _, err := os.Stat(base); err != nil {
			t.Fatalf("stat %s: %v", base, err)
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Design specs and plans are dated historical records, not
				// instructions a user copies.
				if d.Name() == "superpowers" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".md") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(body), "\n") {
				if retiredBackupFlag.MatchString(line) {
					rel, _ := filepath.Rel(root, path)
					hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}
	if len(hits) > 0 {
		t.Errorf("docs use the retired -o/--output flag on a backup command; use --file:\n  %s", strings.Join(hits, "\n  "))
	}
}

func TestRetiredBackupFlagPattern(t *testing.T) {
	bad := []string{
		"kuso backup -o /tmp/kuso.sql.gz",
		"0 4 * * * root kuso backup -o /var/backups/kuso.sql.gz",
		"kuso backup --output x.sql.gz",
		"backup --output <file>        control-plane pg_dump",
		"kuso addon-backup download <p> <addon> [-o dump.sql.gz] [--force]",
		"kuso addon-backup download shop db -o db.sql.gz",
	}
	good := []string{
		"kuso backup --file /tmp/kuso.sql.gz",
		"kuso addon-backup download shop db --file db.sql.gz",
		"kuso addon-backup list shop db -o json",
		"kuso backup health",
	}
	for _, l := range bad {
		if !retiredBackupFlag.MatchString(l) {
			t.Errorf("expected match: %q", l)
		}
	}
	for _, l := range good {
		if retiredBackupFlag.MatchString(l) {
			t.Errorf("unexpected match: %q", l)
		}
	}
}
