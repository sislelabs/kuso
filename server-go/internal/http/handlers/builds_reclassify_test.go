package handlers

import (
	"context"
	"testing"

	"kuso/server/internal/failures"
)

func TestReclassifyGenericFailures_UsesArchivedLogTail(t *testing.T) {
	t.Parallel()
	generic := &failures.Classification{Kind: failures.KindGeneric}
	out := []buildSummary{
		{ID: "b1", Status: "failed", ErrorMessage: "container exited with code 1 (clone)", FailureClass: generic},
		{ID: "b2", Status: "succeeded"},
		{ID: "b3", Status: "failed", FailureClass: &failures.Classification{Kind: failures.KindOOM}},
		{ID: "b4", Status: "failed", ErrorMessage: "fatal: Remote branch gone not found in upstream origin"},
	}
	logs := map[string]string{
		"b1": "Cloning into '/workspace'...\nfatal: Remote branch feature-csv-export not found in upstream origin\n",
	}
	var asked []string
	reclassifyGenericFailures(context.Background(), out, func(_ context.Context, id string) (string, error) {
		asked = append(asked, id)
		return logs[id], nil
	})
	if out[0].FailureClass == nil || out[0].FailureClass.Kind != failures.KindCloneRefMissing {
		t.Errorf("b1 = %+v, want clone_ref_missing from the log tail", out[0].FailureClass)
	}
	if out[2].FailureClass.Kind != failures.KindOOM {
		t.Errorf("a specific stored kind must be kept, got %s", out[2].FailureClass.Kind)
	}
	if out[3].FailureClass == nil || out[3].FailureClass.Kind != failures.KindCloneRefMissing {
		t.Errorf("b4 = %+v, want clone_ref_missing from the error message", out[3].FailureClass)
	}
	if len(asked) != 1 || asked[0] != "b1" {
		t.Errorf("archive reads = %v, want only b1", asked)
	}
}

func TestExplainMissingBranch(t *testing.T) {
	t.Parallel()
	stale := &failures.Classification{Kind: failures.KindCloneRefMissing, Summary: "No action needed.", Remediation: &failures.Remediation{Title: "Nothing to fix — the ref was deleted"}}
	out := []buildSummary{
		{ID: "failed", Status: "failed", Branch: "main", FailureClass: stale},
		{ID: "cancelled", Status: "cancelled", Branch: "feat", FailureClass: stale},
	}
	explainMissingBranch(out)
	if got := out[0].FailureClass; got.Remediation == nil || got.Remediation.Title == stale.Remediation.Title || got.Summary != `Branch "main" no longer exists in the repository.` {
		t.Errorf("failed build kept the cancelled copy: %+v", got)
	}
	if out[1].FailureClass != stale {
		t.Errorf("a cancelled build must keep its copy")
	}
}
