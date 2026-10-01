package handlers

import (
	"regexp"
	"strings"
	"testing"
)

// The pr-1 matcher used to be "<ns>-<env>.*" with no separator, so it
// also counted pr-10..pr-19 traffic.
func TestEnvTraefikMatcher_AnchorsEnvName(t *testing.T) {
	m := envTraefikMatcher("kuso-koreni", "koreni-api-pr-1")
	// The matcher is embedded in a PromQL string; undo that escaping.
	re := regexp.MustCompile("^" + strings.ReplaceAll(m, `\\`, `\`) + "$")
	if !re.MatchString("kuso-koreni-koreni-api-pr-1-http@kubernetes") {
		t.Errorf("own env not matched by %q", m)
	}
	if re.MatchString("kuso-koreni-koreni-api-pr-10-http@kubernetes") {
		t.Errorf("pr-10 matched by pr-1 matcher %q", m)
	}
}
