package handlers

import (
	"context"
	"strings"

	"kuso/server/internal/failures"
)

// reclassifyMax bounds how many archived logs one builds read pulls.
const reclassifyMax = 10

// reclassifyLogLines is the log tail handed to the classifier.
const reclassifyLogLines = 300

// reclassifyGenericFailures re-runs the classifier for failed builds whose
// stored kind is generic (or missing): builds classified before a detector
// existed (clone_ref_missing, …) otherwise keep a vague "Deploy failed"
// forever and the UI offers a Retry that fails the same way. Read-only:
// nothing is written back. fetchLog returns the archived build log ("" when
// none); the extracted error message is tried first so most rows never
// touch the archive.
func reclassifyGenericFailures(ctx context.Context, out []buildSummary, fetchLog func(context.Context, string) (string, error)) {
	fetched := 0
	for i := range out {
		b := &out[i]
		if b.Status != "failed" || (b.FailureClass != nil && b.FailureClass.Kind != failures.KindGeneric) {
			continue
		}
		if c, ok := specificClass([]string{b.ErrorMessage}); ok {
			b.FailureClass = &c
			continue
		}
		if fetchLog == nil || fetched >= reclassifyMax {
			continue
		}
		fetched++
		logs, err := fetchLog(ctx, b.ID)
		if err != nil || logs == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
		if len(lines) > reclassifyLogLines {
			lines = lines[len(lines)-reclassifyLogLines:]
		}
		if c, ok := specificClass(lines); ok {
			b.FailureClass = &c
		}
	}
}

func specificClass(lines []string) (failures.Classification, bool) {
	c := failures.Classify(lines, failures.Signal{})
	return c, c.Kind != "" && c.Kind != failures.KindGeneric
}
