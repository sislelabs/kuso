package projects

import "testing"

func TestSnapshotFieldInChangedAggregate(t *testing.T) {
	c := changedFields{Snapshot: true}
	if !c.any() {
		t.Fatal("changedFields.any() must be true when Snapshot changed")
	}
}
