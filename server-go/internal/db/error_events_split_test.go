package db

import (
	"context"
	"testing"
	"time"
)

// Rows written before the scanner grouped multi-line errors: the second
// line of one write and an indented frame each became their own group.
// Readers hide them; genuine repeats and unrelated neighbours stay.
func TestErrorReadersHideSplitContinuations(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	us := time.Microsecond
	ev := func(fp, line string, at time.Time) ErrorEvent {
		return ErrorEvent{Project: "p", Service: "p-api", Env: "production", Pod: "pod-1", Fingerprint: fp, Message: line, RawLine: line, Ts: at}
	}
	for _, e := range []ErrorEvent{
		ev("one", "ERROR restart probe line one", t0),
		ev("two", "ERROR restart probe line two", t0.Add(22*us)),
		ev("hook", "ERROR payment webhook failed (req 1)", t0.Add(time.Minute)),
		ev("hook", "ERROR payment webhook failed (req 2)", t0.Add(time.Minute+24*us)),
		ev("type", `TypeError: Cannot read properties of undefined`, t0.Add(time.Minute+36*us)),
		ev("frame", "    at Object.<anonymous> (TypeError thrown here)", t0.Add(time.Minute+40*us)),
		ev("receipt", "ERROR failed to send order receipt order_id=1012", t0.Add(2*time.Minute)),
		ev("tb", "Traceback (most recent call last):", t0.Add(2*time.Minute+30*us)),
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	since := t0.Add(-time.Minute)

	groups, err := d.ListErrorGroups(ctx, "p", "p-api", "", since, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, g := range groups {
		got[g.Fingerprint] = g.Count
	}
	want := map[string]int64{"one": 1, "hook": 2, "type": 1, "receipt": 1}
	if len(got) != len(want) {
		t.Fatalf("groups = %v; want %v", got, want)
	}
	for fp, n := range want {
		if got[fp] != n {
			t.Errorf("group %s = %d; want %d (all: %v)", fp, got[fp], n, got)
		}
	}

	sum, err := d.SummarizeProjectErrors(ctx, "p", "", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 1 || sum[0].Occurrences != 5 || sum[0].Groups != 4 {
		t.Errorf("summary = %+v; want 5 occurrences in 4 groups", sum)
	}

	detail, err := d.GetErrorGroupDetail(ctx, "p", "p-api", "", "two", since, time.Hour, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Occurrences) != 0 {
		t.Errorf("hidden group detail = %+v; want no occurrences", detail.Occurrences)
	}
}
