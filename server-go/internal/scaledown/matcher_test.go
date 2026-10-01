package scaledown

import (
	"regexp"
	"testing"
)

func TestTraefikServiceMatcher_DoesNotSpanSiblingPRs(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile("^" + traefikServiceMatcher("kuso", "shop-web-pr-1") + "$")
	if !re.MatchString("kuso-shop-web-pr-1-http@kubernetes") {
		t.Error("pr-1's own service not matched")
	}
	if re.MatchString("kuso-shop-web-pr-12-http@kubernetes") {
		t.Error("pr-1 matcher also counts pr-12's traffic")
	}
}
