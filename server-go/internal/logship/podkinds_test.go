package logship

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/errorscan"
)

// Every listed pod is classified for the error scanner, build pods as
// non-runtime, before any of its lines are shipped.
func TestReconcileRecordsPodKinds(t *testing.T) {
	logs := &fakeLogs{lines: map[string][]fakeLine{}}
	app := testPod("p-svc-production-6d8f-abcde", corev1.PodRunning, false)
	build := testPod("p-svc-main-q8w7e", corev1.PodSucceeded, true)
	build.Labels["app.kubernetes.io/component"] = "kusobuild"
	s, _ := newTestShipper(logs, app, build)
	s.PodKinds = errorscan.NewPodKinds()
	tick(t, s)

	if rt, known := s.PodKinds.Lookup(app.Name); !known || !rt {
		t.Errorf("app pod: (runtime=%v, known=%v), want (true, true)", rt, known)
	}
	if rt, known := s.PodKinds.Lookup(build.Name); !known || rt {
		t.Errorf("build pod: (runtime=%v, known=%v), want (false, true)", rt, known)
	}
}
