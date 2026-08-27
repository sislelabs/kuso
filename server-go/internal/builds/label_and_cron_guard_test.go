package builds

import (
	"strings"
	"testing"
)

// TestClampLabelValue: kube rejects a label value over 63 bytes with a
// 422 rather than truncating it, so the whole build Create failed and
// surfaced as an opaque 500. validateProjectName allows 40 chars and
// serviceNameRE allows 32, so a service FQN can reach 73.
func TestClampLabelValue(t *testing.T) {
	t.Parallel()

	short := "alpha-web"
	if got := clampLabelValue(short); got != short {
		t.Errorf("short value was altered: %q -> %q", short, got)
	}

	project := strings.Repeat("p", 40)
	service := strings.Repeat("s", 32)
	fqn := project + "-" + service // 73 bytes
	got := clampLabelValue(fqn)
	if len(got) > maxLabelValue {
		t.Errorf("clamped value is %d bytes, over kube's %d limit: %q", len(got), maxLabelValue, got)
	}

	// Two long values sharing a prefix must not collide after clamping,
	// or two services would fight over one build CR name.
	a := clampLabelValue(project + "-" + strings.Repeat("s", 31) + "a")
	b := clampLabelValue(project + "-" + strings.Repeat("s", 31) + "b")
	if a == b {
		t.Errorf("two distinct long FQNs clamped to the same value: %q", a)
	}
}

// TestBuildCRName_StaysWithinLimit: the CR name is derived from the same
// project+service pair, and refs.go's comment claimed it "stays under 63"
// — it did not.
func TestBuildCRName_StaysWithinLimit(t *testing.T) {
	t.Parallel()
	name := buildCRName(strings.Repeat("p", 40), strings.Repeat("s", 32), "abc123")
	if len(name) > maxLabelValue {
		t.Errorf("buildCRName returned %d bytes, over the %d limit: %q", len(name), maxLabelValue, name)
	}
}

// TestPromotionBranchMatches_CronGuard pins the semantics promoteToCrons
// relies on: passing an empty envBranch means "production only", so a
// staging build must not repoint production crons.
func TestPromotionBranchMatches_CronGuard(t *testing.T) {
	t.Parallel()
	if promotionBranchMatches("staging", "", "main") {
		t.Error("a staging build was allowed to repoint production crons")
	}
	if promotionBranchMatches("feature/x", "", "main") {
		t.Error("a feature-branch build was allowed to repoint production crons")
	}
	if !promotionBranchMatches("main", "", "main") {
		t.Error("a production-branch build was blocked from repointing crons")
	}
	// An unset build branch keeps its historical pass-through behaviour.
	if !promotionBranchMatches("", "", "main") {
		t.Error("an unset build branch should not be blocked")
	}
}
