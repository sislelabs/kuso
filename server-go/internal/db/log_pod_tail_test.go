package db

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// TestPodLogTail: cronwatch reads a failed Job's last lines from the
// archive after the Job controller has deleted its pod. Only that Job's
// pods ("<job>-xxxxx") count — not a sibling Job whose name extends it,
// not the service's web pods, not lines from before the Job existed.
func TestPodLogTail(t *testing.T) {
	d := openTestDB(t).AsLogDB()
	ctx := context.Background()
	t0 := time.Now().Add(-10 * time.Minute).UTC()
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	lines := []LogLine{
		{Ts: at(-5), Pod: "sched-100-old00", Project: "p", Service: "p-web", Line: "before job"},
		{Ts: at(1), Pod: "sched-100-abcde", Project: "p", Service: "p-web", Line: "one"},
		{Ts: at(2), Pod: "sched-100-abcde", Project: "p", Service: "p-web", Line: "two"},
		{Ts: at(3), Pod: "sched-100-abcde", Project: "p", Service: "p-web", Line: "three"},
		{Ts: at(3), Pod: "sched-1001-zzzzz", Project: "p", Service: "p-web", Line: "sibling job"},
		{Ts: at(4), Pod: "p-web-7d9f-xyz", Project: "p", Service: "p-web", Line: "web pod"},
		{Ts: at(4), Pod: "sched-100-abcde", Project: "q", Service: "p-web", Line: "other project"},
	}
	if err := d.InsertLogLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	got, err := d.PodLogTail(ctx, "p", "p-web", "sched-100-", t0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"two", "three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PodLogTail = %q, want %q (oldest-first, last n)", got, want)
	}
	got, err = d.PodLogTail(ctx, "p", "p-web", "sched-100-", t0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two", "three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PodLogTail(n=10) = %q, want %q (nothing from before since)", got, want)
	}
	// A LIKE wildcard in the prefix must match literally.
	got, err = d.PodLogTail(ctx, "p", "p-web", "sched_100-", t0, 5)
	if err != nil || len(got) != 0 {
		t.Errorf("wildcard prefix matched %q (err %v)", got, err)
	}
}
