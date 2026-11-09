package projects

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"

	"kuso/server/internal/podreason"
)

// PodEvent is one kube event about a service's pods or its Deployment.
// Wire shape for GET /api/projects/{p}/services/{s}/events.
type PodEvent struct {
	// Type is "Normal" or "Warning".
	Type   string `json:"type"`
	Reason string `json:"reason"`
	// Message is the raw kube text; Summary is it in plain words, set
	// only for scheduling failures (FailedScheduling) where the raw text
	// is unreadable.
	Message   string `json:"message"`
	Summary   string `json:"summary,omitempty"`
	Object    string `json:"object"` // "pod/<name>" or "deployment/<name>"
	Count     int32  `json:"count"`
	FirstSeen string `json:"firstSeen,omitempty"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

// PodEventList is the endpoint's response.
type PodEventList struct {
	Namespace string     `json:"namespace"`
	Events    []PodEvent `json:"events"`
}

const (
	podEventsMaxPods = 10
	podEventsMax     = 50
)

// ListPodEvents returns recent events for the env's current pods and its
// Deployment, newest first. One field-selected LIST per object keeps it
// cheap even in a busy shared namespace.
func (s *Service) ListPodEvents(ctx context.Context, project, service, env string) (*PodEventList, error) {
	ns, envCR, err := s.resolveOwnedEnv(ctx, project, service, env)
	if err != nil {
		return nil, err
	}
	pods, err := s.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envCR.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	// Newest pods first: those are the ones a stuck rollout is about.
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[j].CreationTimestamp.Before(&pods.Items[i].CreationTimestamp)
	})
	type target struct{ kind, name string }
	targets := []target{{"Deployment", envCR.Name}}
	for i := range pods.Items {
		if len(targets) > podEventsMaxPods {
			break
		}
		targets = append(targets, target{"Pod", pods.Items[i].Name})
	}

	out := &PodEventList{Namespace: ns, Events: []PodEvent{}}
	for _, t := range targets {
		sel := fields.Set{"involvedObject.kind": t.kind, "involvedObject.name": t.name}.AsSelector().String()
		list, err := s.Kube.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: sel})
		if err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		for i := range list.Items {
			out.Events = append(out.Events, podEventOf(&list.Items[i]))
		}
	}
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].LastSeen > out.Events[j].LastSeen })
	if len(out.Events) > podEventsMax {
		out.Events = out.Events[:podEventsMax]
	}
	return out, nil
}

func podEventOf(e *corev1.Event) PodEvent {
	last := e.LastTimestamp.Time
	if last.IsZero() {
		last = e.EventTime.Time
	}
	first := e.FirstTimestamp.Time
	if first.IsZero() {
		first = last
	}
	count := e.Count
	if count == 0 && e.Series != nil {
		count = e.Series.Count
	}
	if count == 0 {
		count = 1
	}
	kind := "pod"
	if e.InvolvedObject.Kind == "Deployment" {
		kind = "deployment"
	}
	ev := PodEvent{
		Type:      e.Type,
		Reason:    e.Reason,
		Message:   e.Message,
		Object:    kind + "/" + e.InvolvedObject.Name,
		Count:     count,
		FirstSeen: rfc3339(first),
		LastSeen:  rfc3339(last),
	}
	switch e.Reason {
	case "FailedScheduling":
		ev.Summary = podreason.SummarizeScheduling(e.Message)
	case "Failed", "BackOff":
		// "Failed to pull image …" / "Back-off pulling image …".
		if strings.Contains(strings.ToLower(e.Message), "pull") {
			ev.Summary = podreason.Summarize("ErrImagePull", e.Message)
		}
	}
	return ev
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
