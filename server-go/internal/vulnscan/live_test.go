package vulnscan

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/db"
)

func TestPrimaryImage_FirstContainerOfALivePod(t *testing.T) {
	done := pod("kuso", "old", nil, ctrl("ReplicaSet", "web-1"), "r/web:0", "r/web@"+dC)
	done.Status.Phase = corev1.PodSucceeded
	live := pod("kuso", "new", map[string]string{"pod-template-hash": "1"}, ctrl("ReplicaSet", "web-1"), "r/web:1", "r/web@"+dA)
	live.Spec.Containers = append(live.Spec.Containers, corev1.Container{Name: "sidecar", Image: "envoy:1"})
	live.Status.ContainerStatuses = append([]corev1.ContainerStatus{{Name: "sidecar", ImageID: "envoy@" + dB}}, live.Status.ContainerStatuses...)

	im := primaryImage([]corev1.Pod{done, live}, db.VulnImage{OwnerKind: db.VulnOwnerService, Project: "p", Service: "web"})
	if im == nil || im.Digest != dA || im.ImageRef != "r/web@"+dA || im.Workload != "web" || im.Namespace != "kuso" {
		t.Fatalf("primary = %+v", im)
	}
	if primaryImage([]corev1.Pod{done}, db.VulnImage{}) != nil {
		t.Error("no live pod → nil")
	}
}

func TestLatestPerOwner_DropsReplacedDigests(t *testing.T) {
	now := time.Now()
	rows := []db.VulnImage{
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dA, LastSeenAt: now},
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dC, LastSeenAt: now}, // init container, same sweep
		{OwnerKind: "service", Service: "web", Env: "production", Workload: "w", Digest: dB, LastSeenAt: now.Add(-24 * time.Hour)},
		{OwnerKind: "service", Service: "web", Env: "staging", Workload: "s", Digest: dB, LastSeenAt: now.Add(-24 * time.Hour)},
	}
	got := LatestPerOwner(rows)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, im := range got {
		if im.Env == "production" && im.Digest == dB {
			t.Errorf("replaced production digest kept: %+v", im)
		}
	}
}
