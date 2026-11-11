package activator

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// startFailure: the env's newest pod is stuck in a waiting state it won't
// leave without a new deploy, so holding the request any longer only
// delays an error. The wake itself is not undone.
type startFailure struct{ reason string }

func (e *startFailure) Error() string { return "activator: newest pod cannot start: " + e.reason }

type podFailEntry struct {
	reason string
	at     time.Time
}

// podFailTTL bounds how often held requests for one env LIST its pods.
const podFailTTL = 2 * time.Second

// crashLoopMinRestarts: a crash during a wake can be a dependency that
// isn't up yet and the next restart succeeds; only a pod that has looped
// a few times (≥70s of back-off, past any hold budget) is failed fast.
const crashLoopMinRestarts = 3

// startFailed returns a startFailure when the env's newest pod is in a
// terminal waiting state, else nil. Unknown (LIST error, RBAC without
// pods list) is nil: the request keeps holding as before.
func (a *Activator) startFailed(ctx context.Context, ns, name string) *startFailure {
	if a.podCheckOff.Load() || a.kc == nil || a.kc.Clientset == nil {
		return nil
	}
	key := ns + "/" + name
	now := a.now()
	a.podFailMu.Lock()
	if e, ok := a.podFail[key]; ok && now.Sub(e.at) < podFailTTL {
		a.podFailMu.Unlock()
		return asStartFailure(e.reason)
	}
	a.podFailMu.Unlock()

	pods, err := a.kc.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + name,
	})
	if apierrors.IsForbidden(err) {
		// The ClusterRole in deploy/kuso-activator.yaml is applied by hand,
		// so an older install lacks pods list. Stop asking.
		a.podCheckOff.Store(true)
		return nil
	}
	if err != nil {
		return nil
	}
	reason := newestPodStartFailure(pods.Items)
	a.podFailMu.Lock()
	a.podFail[key] = podFailEntry{reason: reason, at: now}
	if len(a.podFail) > 4096 {
		for k, e := range a.podFail {
			if now.Sub(e.at) >= podFailTTL {
				delete(a.podFail, k)
			}
		}
	}
	a.podFailMu.Unlock()
	return asStartFailure(reason)
}

func asStartFailure(reason string) *startFailure {
	if reason == "" {
		return nil
	}
	return &startFailure{reason: reason}
}

// newestPodStartFailure inspects only the newest live pod: an older
// ReplicaSet's pod being fine (or broken) says nothing about the revision
// a wake actually starts.
func newestPodStartFailure(pods []corev1.Pod) string {
	var newest *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if newest == nil || newest.CreationTimestamp.Before(&p.CreationTimestamp) {
			newest = p
		}
	}
	if newest == nil {
		return ""
	}
	statuses := append(append([]corev1.ContainerStatus{}, newest.Status.InitContainerStatuses...), newest.Status.ContainerStatuses...)
	for _, cs := range statuses {
		w := cs.State.Waiting
		if w == nil {
			continue
		}
		switch w.Reason {
		case "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull", "CreateContainerConfigError":
			return w.Reason
		case "CrashLoopBackOff":
			if cs.RestartCount >= crashLoopMinRestarts {
				return w.Reason
			}
		}
	}
	return ""
}
