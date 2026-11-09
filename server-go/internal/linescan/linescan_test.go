package linescan

import (
	"bufio"
	"strings"
	"testing"
	"unicode/utf8"
)

// LOOP-06: one line past the cap must not end the scan; the lines after
// it still arrive and the long one comes back truncated.
func TestLinesSurvivesOverlongLine(t *testing.T) {
	long := "2026-01-01T00:00:00Z " + strings.Repeat("я", 400) // 2-byte runes
	in := "first\n" + long + "\nafter\nlast"
	sc := bufio.NewScanner(strings.NewReader(in))
	sc.Buffer(make([]byte, 16), 512)
	sc.Split(Lines(101))
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan err: %v", err)
	}
	if len(got) != 4 || got[0] != "first" || got[2] != "after" || got[3] != "last" {
		t.Fatalf("lines = %q", got)
	}
	if !strings.HasPrefix(got[1], "2026-01-01T00:00:00Z ") || len(got[1]) > 101 || !utf8.ValidString(got[1]) {
		t.Errorf("truncated line = %q (len %d)", got[1], len(got[1]))
	}
}

func TestKubeletNotice(t *testing.T) {
	if got := KubeletNotice("unable to retrieve container logs for containerd://abc"); got != "WARN kubelet: unable to retrieve container logs for containerd://abc" {
		t.Errorf("got %q", got)
	}
	if got := KubeletNotice("GET / 200"); got != "GET / 200" {
		t.Errorf("app line changed: %q", got)
	}
}
