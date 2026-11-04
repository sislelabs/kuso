package main

import "testing"

// LOOP-20: after a lease flap the previous tenure's cleanup goroutine can
// run after the new tenure started. It must then do nothing, or it turns
// leaderActive off and unregisters the new tenure's loops.
func TestLeaseTenureStaleCleanupIsNoop(t *testing.T) {
	var lt leaseTenure
	active := false
	old := lt.begin(func() { active = true })
	cur := lt.begin(func() { active = true })

	lt.end(old, func() { active = false })
	if !active {
		t.Fatal("stale tenure cleanup cleared the current tenure's state")
	}
	lt.end(cur, func() { active = false })
	if active {
		t.Fatal("current tenure's own cleanup did not run")
	}
}
