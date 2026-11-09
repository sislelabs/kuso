package projects

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodEventOf_SummarizesSchedulingFailure(t *testing.T) {
	now := time.Now()
	e := &corev1.Event{
		Type:           "Warning",
		Reason:         "FailedScheduling",
		Message:        "0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }.",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "demo-api-production-abc"},
		LastTimestamp:  metav1.NewTime(now),
	}
	got := podEventOf(e)
	if got.Summary != "The node is low on disk, so the pod can't be scheduled" {
		t.Errorf("summary = %q", got.Summary)
	}
	if got.Object != "pod/demo-api-production-abc" || got.Count != 1 || got.FirstSeen == "" {
		t.Errorf("event = %+v", got)
	}
	normal := podEventOf(&corev1.Event{Type: "Normal", Reason: "Pulled", Message: `Container image "x" already present`})
	if normal.Summary != "" {
		t.Errorf("normal event got a summary: %q", normal.Summary)
	}
}
