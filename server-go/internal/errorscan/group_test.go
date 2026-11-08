package errorscan

import (
	"strings"
	"testing"
	"time"
)

type emitted struct {
	title string
	raw   string
}

// runGrouper feeds lines (one pod unless prefixed "podX|") spaced by the
// given gaps and returns the emitted occurrences.
func runGrouper(t *testing.T, gaps []time.Duration, lines ...string) []emitted {
	t.Helper()
	var out []emitted
	g := newGrouper(func(c *errorChain) { out = append(out, emitted{c.title, c.raw()}) })
	ts := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	for i, line := range lines {
		pod := "p"
		if before, after, ok := strings.Cut(line, "|"); ok && strings.HasPrefix(before, "pod") {
			pod, line = before, after
		}
		if i > 0 {
			gap := 20 * time.Microsecond
			if i-1 < len(gaps) && gaps[i-1] != 0 {
				gap = gaps[i-1]
			}
			ts = ts.Add(gap)
		}
		g.feed(scanLine{id: int64(i + 1), ts: ts, pod: pod, line: line}, matchesAnyPattern(line))
	}
	g.flush()
	return out
}

func TestGrouper_SameWriteSiblingLinesAreOneError(t *testing.T) {
	got := runGrouper(t, nil, "ERROR restart probe line one", "ERROR restart probe line two")
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	if got[0].raw != "ERROR restart probe line one\nERROR restart probe line two" {
		t.Fatalf("raw = %q", got[0].raw)
	}
}

func TestGrouper_RepeatsInOneBurstStaySeparate(t *testing.T) {
	got := runGrouper(t, nil,
		"ERROR payment webhook failed: upstream timeout after 3000ms (req 30954)",
		"ERROR payment webhook failed: upstream timeout after 3000ms (req 30703)",
		`TypeError: Cannot read properties of undefined (reading "id")`,
	)
	if len(got) != 3 {
		t.Fatalf("want 3 errors, got %d: %+v", len(got), got)
	}
}

func TestGrouper_NodeStack(t *testing.T) {
	got := runGrouper(t, nil,
		`TypeError: Cannot read properties of undefined (reading "id")`,
		"    at handler (/app/server.js:12:5)",
		"    at processTicksAndRejections (node:internal/process/task_queues:95:5)",
		"GET /healthz 200",
	)
	if len(got) != 1 || strings.Count(got[0].raw, "\n") != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestGrouper_PythonTracebackNamedAfterException(t *testing.T) {
	got := runGrouper(t, nil,
		"Traceback (most recent call last):",
		`  File "/app/main.py", line 3, in <module>`,
		"    run()",
		"ValueError: bad input",
		"INFO next request",
	)
	if len(got) != 1 {
		t.Fatalf("want 1, got %+v", got)
	}
	if got[0].title != "ValueError: bad input" {
		t.Fatalf("title = %q", got[0].title)
	}
}

func TestGrouper_GoPanic(t *testing.T) {
	got := runGrouper(t, nil,
		"panic: runtime error: invalid memory address or nil pointer dereference",
		"[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x4a1b2c]",
		"",
		"goroutine 1 [running]:",
		"main.main()",
		"\t/app/main.go:12 +0x1d",
		"exit status 2",
	)
	if len(got) != 1 || !strings.HasSuffix(got[0].raw, "exit status 2") {
		t.Fatalf("got %+v", got)
	}
}

func TestGrouper_LateIndentedLineStartsNothing(t *testing.T) {
	got := runGrouper(t, []time.Duration{time.Second}, "ERROR boom", "    indented later")
	if len(got) != 1 || got[0].raw != "ERROR boom" {
		t.Fatalf("got %+v", got)
	}
}

func TestGrouper_PodsDontInterleave(t *testing.T) {
	got := runGrouper(t, nil, "podA|ERROR boom on a", "podB|    at somewhere", "podA|    at frame")
	if len(got) != 1 || got[0].raw != "ERROR boom on a\n    at frame" {
		t.Fatalf("got %+v", got)
	}
}

func TestGrouper_SiblingRuleNeedsSameWrite(t *testing.T) {
	got := runGrouper(t, []time.Duration{50 * time.Millisecond}, "ERROR restart probe line one", "ERROR restart probe line two")
	if len(got) != 2 {
		t.Fatalf("want 2, got %+v", got)
	}
}
