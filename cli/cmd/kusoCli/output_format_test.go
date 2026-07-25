package kusoCli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSharedOutputFormatDefaultIsTable pins the aliasing hazard behind
// the "-o table is ignored" bug.
//
// pflag writes a flag's default into the bound variable at REGISTRATION
// time, so when many commands bind one shared variable the last init()
// to run decides the value for all of them — and init() order is
// alphabetical by filename, which no command author can reason about
// locally. Four JSON-only commands registered "json" against the shared
// outputFormat; user.go sorts last, so the effective default for every
// one of the ~48 "table" commands silently became json. `kuso build
// list` emitted JSON while its own --help advertised (default "table").
//
// If this fails, a command has bound &outputFormat with a non-"table"
// default. Bind &outputFormatJSONOnly instead.
func TestSharedOutputFormatDefaultIsTable(t *testing.T) {
	if outputFormat != "table" {
		t.Fatalf("shared outputFormat resolved to %q after init(); every command "+
			"registering default \"table\" would silently emit %q", outputFormat, outputFormat)
	}
}

// Every registration of the SHARED variable must agree on "table".
// Commands with their own output variable (logs search -o text,
// notifications get -o pretty, the JSON-only four) are out of scope —
// this scans the source for binds of &outputFormat specifically, which
// is the only set that can clobber each other.
func TestNoCommandRegistersNonTableDefaultOnSharedVar(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	re := regexp.MustCompile(`StringVarP\(&outputFormat,\s*"output",\s*"o",\s*"([a-z]*)"`)
	var bad []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if m[1] != "table" {
				bad = append(bad, f+" default="+m[1])
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("these bind the SHARED outputFormat with a non-table default, which "+
			"silently becomes the default for every other command depending on init() "+
			"order — bind &outputFormatJSONOnly instead: %s", strings.Join(bad, ", "))
	}
}
