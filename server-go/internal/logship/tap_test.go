package logship

import (
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/db"
)

type recordingTap struct {
	mu    sync.Mutex
	lines []TappedLine
}

func (r *recordingTap) Tap(l TappedLine) {
	r.mu.Lock()
	r.lines = append(r.lines, l)
	r.mu.Unlock()
}

func TestTapSeesAcceptedLinesWithKubeletTimestamp(t *testing.T) {
	emitted := time.Date(2026, 9, 23, 12, 0, 0, 123, time.UTC)
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	logs.add("web", "main", emitted, "hello")
	pod := testPod("web", corev1.PodSucceeded, true)
	pod.Labels["app.kubernetes.io/instance"] = "p-svc-production"
	pod.Labels["kuso.sislelabs.com/env-kind"] = "production"
	s, _ := newTestShipper(logs, pod)
	rt := &recordingTap{}
	s.Tap = rt

	tick(t, s)

	if len(rt.lines) != 1 {
		t.Fatalf("tap saw %d lines, want 1", len(rt.lines))
	}
	got := rt.lines[0]
	if got.Line != "hello" || got.Pod != "web" || got.Project != "p" || got.Service != "svc" {
		t.Fatalf("unexpected tapped line: %+v", got)
	}
	if !got.Emitted.Equal(emitted) {
		t.Fatalf("Emitted=%v, want kubelet ts %v", got.Emitted, emitted)
	}
	if got.EnvName != "p-svc-production" || got.EnvKind != "production" {
		t.Fatalf("env metadata not carried: %+v", got)
	}
}

func TestTapSkipsRateCappedLines(t *testing.T) {
	t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN", "1")
	s, _ := newTestShipper(&fakeLogs{lines: map[string][]fakeLine{}})
	rt := &recordingTap{}
	s.Tap = rt
	s.append(db.LogLine{Project: "p", Service: "svc", Line: "a"}, time.Time{}, "", "")
	s.append(db.LogLine{Project: "p", Service: "svc", Line: "b"}, time.Time{}, "", "")
	if len(rt.lines) != 1 || rt.lines[0].Line != "a" {
		t.Fatalf("tap must only see lines the DB buffer accepted, got %+v", rt.lines)
	}
}
