package logs

import "testing"

// --lines N used to be split across replicas (3 lines on two pods gave 2)
// and pods were concatenated unordered. Lines from every pod are now
// merged in kubelet-timestamp order before the newest N are kept.
func TestMergeByTime(t *testing.T) {
	t.Parallel()
	var lines []Line
	for _, raw := range []struct{ pod, line string }{
		{"a", "2026-10-01T09:00:01.000000000Z a1"},
		{"a", "2026-10-01T09:00:03.000000000Z a2"},
		{"b", "2026-10-01T09:00:02.000000000Z b1"},
		{"b", "2026-10-01T09:00:04.000000000Z b2"},
	} {
		ts, l := splitTimestamp(raw.line)
		if ts.IsZero() {
			t.Fatalf("timestamp not parsed from %q", raw.line)
		}
		lines = append(lines, Line{Pod: raw.pod, Line: l, ts: ts})
	}
	got := mergeByTime(lines)
	want := []string{"a1", "b1", "a2", "b2"}
	for i, w := range want {
		if got[i].Line != w {
			t.Fatalf("merged order = %v, want %v", got, want)
		}
	}
	if _, l := splitTimestamp("no timestamp here"); l != "no timestamp here" {
		t.Errorf("undated line altered: %q", l)
	}
}
