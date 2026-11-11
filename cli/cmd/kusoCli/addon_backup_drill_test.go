package kusoCli

import "testing"

// A failed or count-less drill must exit non-zero so a scheduled drill
// can alert on it.
func TestDrillOutcome(t *testing.T) {
	n := int64(3)
	if _, err := drillOutcome(drillStatus{Phase: "Succeeded", Tables: &n, Rows: &n}); err != nil {
		t.Errorf("success: %v", err)
	}
	for _, st := range []drillStatus{
		{Phase: "Failed", Message: "MISMATCH"},
		{Phase: "Succeeded"},
	} {
		if _, err := drillOutcome(st); err == nil {
			t.Errorf("%+v: want error", st)
		}
	}
}
