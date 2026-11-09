package health

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/notify"
)

func pullingPod(name, reason, message, image string) *corev1.Pod {
	p := svcPod(name, reason)
	p.Status.ContainerStatuses[0].Image = image
	p.Status.ContainerStatuses[0].State.Waiting.Message = message
	return p
}

func onlyCrash(t *testing.T, h *crashHarness) notify.Event {
	t.Helper()
	var got []notify.Event
	for _, e := range h.events {
		if e.Type == notify.EventPodCrashed {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("pod.crashed events = %d, want 1: %+v", len(got), h.events)
	}
	return got[0]
}

// A typo'd tag while the old version serves is a stuck rollout with a
// tag hint, not "Crashing… check the registry credentials". BackOff's
// own message has no cause, so it comes from the pod's Failed event.
func TestPullFailure_MissingTagWhileOldServes(t *testing.T) {
	h := newCrashHarness(t)
	ctx := context.Background()
	if _, err := h.cs.CoreV1().Events(testNS).Create(ctx, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev1", Namespace: testNS},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-new"},
		Reason:         "Failed",
		Message:        `Failed to pull image "registry.local/shop-api:v2-typo": rpc error: code = NotFound desc = failed to resolve reference: registry.local/shop-api:v2-typo: not found`,
		LastTimestamp:  metav1.NewTime(h.now),
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.tick(t, svcPod("api-old", ""), pullingPod("api-new", "ImagePullBackOff", `Back-off pulling image "registry.local/shop-api:v2-typo"`, "registry.local/shop-api:v2-typo"))

	e := onlyCrash(t, h)
	if !strings.HasPrefix(e.Title, "✗ Rollout stuck · ") {
		t.Errorf("title = %q, want Rollout stuck", e.Title)
	}
	s := e.Classification.Summary
	if !strings.Contains(s, "tag v2-typo not found") || !strings.Contains(s, "old version still serving") || strings.Contains(s, "credentials") {
		t.Errorf("summary = %q, want the missing tag, the old version serving and no credentials hint", s)
	}
}

func TestPullFailure_UnauthorizedNamesCredentials(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, pullingPod("api-new", "ErrImagePull",
		`failed to pull and unpack image "ghcr.io/acme/api:v3": failed to authorize: 403 Forbidden`, "ghcr.io/acme/api:v3"))
	e := onlyCrash(t, h)
	if !strings.HasPrefix(e.Title, "✗ Image pull failed · ") {
		t.Errorf("title = %q, want Image pull failed", e.Title)
	}
	if s := e.Classification.Summary; !strings.Contains(s, "401/403") || !strings.Contains(s, "credentials") {
		t.Errorf("summary = %q, want a credentials hint", s)
	}
}

func TestPullFailure_CrashLoopStillSaysCrashing(t *testing.T) {
	h := newCrashHarness(t)
	h.tick(t, svcPod("api-new", "CrashLoopBackOff"))
	if e := onlyCrash(t, h); !strings.HasPrefix(e.Title, "✗ Crashing · ") {
		t.Errorf("title = %q, want Crashing", e.Title)
	}
}
