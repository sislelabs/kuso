package handlers

import (
	"net/http"
	"testing"

	"kuso/server/internal/reconcilehealth"
)

// CLI-32: remediate used to act on the first issue whose Resource matched,
// ignoring the namespace the caller sent.
func TestMatchRemediateIssue(t *testing.T) {
	t.Parallel()
	issues := []reconcilehealth.Issue{
		{Resource: "db", Namespace: "kuso", Type: "addon", Kind: reconcilehealth.KindReleaseFailed, Action: reconcilehealth.ActionForceReconcile},
		{Resource: "db", Namespace: "team-a", Type: "addon", Kind: reconcilehealth.KindReleaseFailed, Action: reconcilehealth.ActionForceReconcile},
		{Resource: "web", Namespace: "kuso", Type: "environment", Kind: reconcilehealth.KindSpecMismatch},
		{Resource: "web", Namespace: "kuso", Type: "environment", Kind: reconcilehealth.KindImmutableVCT, Action: reconcilehealth.ActionOrphanRecreate},
	}
	iss, _, _ := matchRemediateIssue(issues, remediateRequest{Resource: "db", Namespace: "team-a"})
	if iss == nil || iss.Namespace != "team-a" {
		t.Fatalf("namespace team-a picked %+v", iss)
	}
	if iss, status, msg := matchRemediateIssue(issues, remediateRequest{Resource: "db"}); iss != nil || status != http.StatusConflict {
		t.Fatalf("ambiguous name: got %+v %d %q, want 409", iss, status, msg)
	}
	if iss, _, _ := matchRemediateIssue(issues, remediateRequest{Resource: "web"}); iss == nil || iss.Kind != reconcilehealth.KindImmutableVCT {
		t.Fatalf("single remediable issue should be picked without narrowing, got %+v", iss)
	}
	if iss, _, _ := matchRemediateIssue(issues, remediateRequest{Resource: "web", Kind: "spec_mismatch"}); iss == nil || iss.Kind != reconcilehealth.KindSpecMismatch {
		t.Fatalf("kind narrowing, got %+v", iss)
	}
	if _, status, _ := matchRemediateIssue(issues, remediateRequest{Resource: "db", Namespace: "nope"}); status != http.StatusNotFound {
		t.Fatalf("no match status %d, want 404", status)
	}
}
