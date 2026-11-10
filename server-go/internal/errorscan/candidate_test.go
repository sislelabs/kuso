package errorscan

import (
	"encoding/json"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Postgres filters LogLine with candidateLineRe before the Go patterns
// see a line, so any line matchesAnyPattern accepts must also match the
// candidate regex, or that error is silently never scanned. Go RE2 and
// Postgres ARE agree on this plain alternation, so the check runs here.
func TestCandidateRegexIsSupersetOfPatterns(t *testing.T) {
	t.Parallel()
	cand := regexp.MustCompile(`(?i)` + candidateLineRe)

	corpus := []string{
		"2026-08-17 12:00:00 ERROR failed to connect",
		"FATAL: database is not accepting connections",
		"panic: runtime error: index out of range",
		"Traceback (most recent call last):",
		"Exception: invalid literal for int()",
		"java.lang.NullPointerException",
		"UnhandledPromiseRejection: TypeError",
		"UnhandledRejection",
		`Exception in thread "main"`,
		"ActiveRecord::RecordNotFound",
		"ActionController::RoutingError",
		`{"level":"info","status":500,"path":"/api/x"}`,
		`{"status" :  503}`,
		"[emerg] bind failed", "[alert] worker exited", "[crit] ssl", "[CRITICAL] x",
		"AggregateError [ECONNREFUSED]", "SEVERE: x", "smtplib.SMTPServerDisconnected: closed",
	}
	if raw, err := os.ReadFile("testdata/levels.json"); err == nil {
		var f struct {
			Lines []struct {
				Line string `json:"line"`
			} `json:"lines"`
		}
		if json.Unmarshal(raw, &f) == nil {
			for _, l := range f.Lines {
				corpus = append(corpus, l.Line)
			}
		}
	}
	// Random lines built from pattern fragments in random case.
	frags := []string{"error", "Error", "fatal", "severe", "panic: ", "[emerg]", "[alert]", "[crit]",
		"[critical]", "Traceback (most recent call last)", "Exception", "ValueError", "Exception in thread",
		"java.lang.FooException", "UnhandledRejection", "ActionController::", "ActiveRecord::",
		`"status": 502`, " ", ":", "x", "[", "]", "level=info ", "\"", "=", "{", "}"}
	r := rand.New(rand.NewSource(1))
	for range 20000 {
		var b strings.Builder
		for range 1 + r.Intn(5) {
			f := frags[r.Intn(len(frags))]
			if r.Intn(3) == 0 {
				f = strings.ToUpper(f)
			}
			b.WriteString(f)
		}
		corpus = append(corpus, b.String())
	}

	for _, line := range corpus {
		if matchesAnyPattern(line) && !cand.MatchString(line) {
			t.Errorf("error line %q is not a SQL candidate; extend candidateLineRe", line)
		}
	}
}
