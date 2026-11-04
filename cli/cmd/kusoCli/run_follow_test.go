package kusoCli

import (
	"fmt"
	"reflect"
	"testing"
)

// Simulates `kuso run -f` against the server's bounded tail: the log
// grows past the window size, and every poll must print exactly the
// lines that are new since the previous one.
func TestRunLogFollower_PrintsPastWindow(t *testing.T) {
	const window = 500
	var full []string
	for i := 0; i < 2300; i++ {
		full = append(full, fmt.Sprintf("line %d", i))
	}
	tail := func(n int) []string {
		if n <= window {
			return full[:n]
		}
		return full[n-window : n]
	}

	var f runLogFollower
	var printed []string
	for _, n := range []int{0, 120, 480, 700, 1000, 1000, 1400, 1850, 2300, 2300} {
		fresh, gap := f.next(tail(n))
		if gap {
			t.Fatalf("unexpected gap at n=%d", n)
		}
		printed = append(printed, fresh...)
	}
	if !reflect.DeepEqual(printed, full) {
		t.Fatalf("printed %d lines, want %d (first mismatch around %v)", len(printed), len(full), firstDiff(printed, full))
	}
}

// More output than one window between polls can't be recovered from a
// tail; the follower must flag the gap and still print the newest tail.
func TestRunLogFollower_FlagsGap(t *testing.T) {
	var f runLogFollower
	f.next([]string{"a", "b", "c"})
	fresh, gap := f.next([]string{"x", "y", "z"})
	if !gap || !reflect.DeepEqual(fresh, []string{"x", "y", "z"}) {
		t.Fatalf("fresh=%v gap=%v", fresh, gap)
	}
}

// Repeated identical lines must not be dropped once the log has grown.
func TestRunLogFollower_RepeatedLines(t *testing.T) {
	var f runLogFollower
	f.next([]string{"tick", "tick"})
	fresh, gap := f.next([]string{"tick", "tick", "tick"})
	if gap || !reflect.DeepEqual(fresh, []string{"tick"}) {
		t.Fatalf("fresh=%v gap=%v", fresh, gap)
	}
}

func firstDiff(a, b []string) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	return len(a)
}
