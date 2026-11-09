package errorscan

import (
	"encoding/json"
	"os"
	"testing"
)

// TestLevelAgreementFixture pins errorscan's side of the fixture the web
// log viewer also reads (web/src/components/logs/logLevel.test.ts), so the
// Errors tab and the viewer's error-line count can't drift apart silently.
func TestLevelAgreementFixture(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/levels.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Lines []struct {
			Line      string `json:"line"`
			Errorscan bool   `json:"errorscan"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Lines) < 20 {
		t.Fatalf("fixture has %d lines, want the full table", len(f.Lines))
	}
	for _, c := range f.Lines {
		if got := matchesAnyPattern(c.Line); got != c.Errorscan {
			t.Errorf("matchesAnyPattern(%q) = %v, fixture says %v", c.Line, got, c.Errorscan)
		}
	}
}
