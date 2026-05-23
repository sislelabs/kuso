// Package health is the in-cluster watchdog. Every interval it
// surveys node disk usage and pod status across the kuso namespace,
// firing notify events when something crosses a threshold or
// transitions to a bad state.
//
// We deliberately don't use prometheus alertmanager here — the
// install footprint is too big for a single Hetzner box, and the
// signals we care about (disk, crash loops, image pull errors) are
// trivially observable via the kube API. Prometheus stays focused on
// request/error/latency timeseries; this watcher handles operational
// alerts.
//
// State is kept in-memory: we remember which alerts we've already
// fired so a CrashLoopBackOff that lasts an hour doesn't spam
// Discord every 30s. On restart we re-emit the current state once,
// which is fine — operators want the boot-time summary anyway.
package health

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

// Watcher polls cluster state every Interval and emits notify events.
// Construct via New, run via Run in a goroutine.
type Watcher struct {
	Kube      *kube.Client
	Namespace string
	Notify    *notify.Dispatcher
	Logger    *slog.Logger
	Interval  time.Duration

	// DiskWarnPct is the node-disk-used % threshold above which we
	// fire alert.fired. Default 85.
	DiskWarnPct int

	mu     sync.Mutex
	fired  map[string]bool // alert key → was already fired
}

// New returns a Watcher with sensible defaults.
func New(k *kube.Client, ns string, n *notify.Dispatcher, logger *slog.Logger) *Watcher {
	return &Watcher{
		Kube:        k,
		Namespace:   ns,
		Notify:      n,
		Logger:      logger,
		Interval:    60 * time.Second,
		DiskWarnPct: 85,
		fired:       map[string]bool{},
	}
}

// Run loops until ctx is cancelled. First tick fires immediately so
// boot-time issues surface within a minute, not after the interval.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	w.checkPods(ctx)
	w.checkNodes(ctx)
}

// checkPods finds pods in CrashLoopBackOff / ImagePullBackOff /
// CreateContainerConfigError and fires once per (pod, reason). When
// the pod recovers we forget the key so a later relapse alerts again.
func (w *Watcher) checkPods(ctx context.Context) {
	pods, err := w.Kube.Clientset.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger.Warn("health: list pods", "err", err)
		return
	}
	live := map[string]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		reason := podBadReason(p)
		if reason == "" {
			continue
		}
		key := "pod:" + p.Name + ":" + reason
		live[key] = true
		w.mu.Lock()
		already := w.fired[key]
		w.fired[key] = true
		w.mu.Unlock()
		if already {
			continue
		}
		project := p.Labels["kuso.sislelabs.com/project"]
		service := p.Labels["app.kubernetes.io/instance"]
		envKind := p.Labels["kuso.sislelabs.com/env-kind"]
		restarts := containerRestartTotal(p)
		// Pull 50 lines for the classifier; the Discord card still only
		// shows the last 5 (joined via previousLogTail). The classifier
		// walks the larger window in reverse to find the regex that
		// matches the failure — 5 lines is too small when nixpacks /
		// buildpacks chatter after the actual error.
		logLines := w.previousLogLines(p, reason, 50)
		// Derive the short tail from the same slice so card + classifier
		// can't disagree about what "the tail" was.
		logTail := ""
		if n := len(logLines); n > 0 {
			start := n - 5
			if start < 0 {
				start = 0
			}
			logTail = strings.Join(logLines[start:], "\n")
		}
		// Stripping "init:" off the reason for the classifier — the
		// signal taxonomy doesn't distinguish init vs main containers
		// (the user cares about "image-pull failed", not "image pull
		// failed in init container").
		sigReason := strings.TrimPrefix(reason, "init:")
		classification := failures.Classify(logLines, failures.Signal{Reason: sigReason})
		w.Notify.Emit(notify.PodCrashed(project, service, p.Name, reason, envKind, logTail, restarts, &classification))
	}
	// Garbage-collect stale alerts so a recovered pod can re-alert
	// later.
	w.mu.Lock()
	for k := range w.fired {
		if strings.HasPrefix(k, "pod:") && !live[k] {
			delete(w.fired, k)
		}
	}
	w.mu.Unlock()
}

func podBadReason(p *corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			r := cs.State.Waiting.Reason
			if r == "CrashLoopBackOff" || r == "ImagePullBackOff" || r == "CreateContainerConfigError" || r == "ErrImagePull" {
				return r
			}
		}
	}
	for _, cs := range p.Status.InitContainerStatuses {
		if cs.State.Waiting != nil {
			r := cs.State.Waiting.Reason
			if r == "CrashLoopBackOff" || r == "ImagePullBackOff" || r == "CreateContainerConfigError" || r == "ErrImagePull" {
				return "init:" + r
			}
		}
	}
	return ""
}

// checkNodes pulls node usage from metrics-server (already in the
// cluster for the metrics panel) and the node's allocatable storage,
// then alerts when used % is past the threshold. Best-effort: any
// error → skip this tick so a flaky kubelet doesn't spam.
func (w *Watcher) checkNodes(ctx context.Context) {
	// Prefer the shared informer's local view — health.Watcher ticks
	// every 60s and the cluster-wide LIST is ~500ms on a 50-node
	// install. Fall back to a live LIST during the cold-boot sync
	// window.
	var nodeList []*corev1.Node
	if cached, ok := w.Kube.Cache.ListNodes(); ok {
		nodeList = cached
	} else {
		nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return
		}
		nodeList = make([]*corev1.Node, len(nodes.Items))
		for i := range nodes.Items {
			nodeList[i] = &nodes.Items[i]
		}
	}
	for _, n := range nodeList {
		// We don't have a direct disk-used metric from kube — the
		// metrics-server exposes CPU + mem only. We approximate by
		// reading the node condition list: kubelet sets
		// `DiskPressure=True` when its eviction threshold is hit.
		// That's a coarser signal than a percentage but it's the one
		// that actually matters operationally (eviction is imminent).
		key := "node:" + n.Name + ":disk-pressure"
		pressure := false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeDiskPressure && c.Status == corev1.ConditionTrue {
				pressure = true
				break
			}
		}
		w.mu.Lock()
		already := w.fired[key]
		if pressure {
			w.fired[key] = true
		} else {
			delete(w.fired, key)
		}
		w.mu.Unlock()
		if pressure && !already {
			w.Notify.Emit(notify.AlertFired(
				fmt.Sprintf("⚠ Node disk pressure: %s", n.Name),
				"kubelet flagged DiskPressure=True. Free up space or pods will start getting evicted.",
				"warn",
				map[string]string{"node": n.Name},
			))
		}
	}
}

// containerRestartTotal sums RestartCount across every container in a
// pod. Init containers are included — an init-loop crash is just as
// alert-worthy as a main-container one, and the user wants the full
// picture in the Discord card.
func containerRestartTotal(p *corev1.Pod) int {
	var total int32
	for _, cs := range p.Status.ContainerStatuses {
		total += cs.RestartCount
	}
	for _, cs := range p.Status.InitContainerStatuses {
		total += cs.RestartCount
	}
	return int(total)
}

// previousLogTail pulls the last ~5 lines of the previous container's
// stdout for a crashing pod. "Previous" matters because the current
// container is in waiting/backoff — its logs are empty; the prior run
// is where the actual error landed.
//
// Best-effort: 5s timeout, kube errors swallowed, empty string when
// no previous run exists (first-boot ImagePullBackOff yields nothing
// to tail anyway). The notify renderer drops empty log tails cleanly.
func (w *Watcher) previousLogTail(p *corev1.Pod, reason string) string {
	lines := w.previousLogLines(p, reason, 5)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// previousLogLines is the slice-returning primitive that backs
// previousLogTail and the failure classifier. The classifier walks
// the slice in reverse looking for known regex patterns; the Discord
// card joins the last 5 lines for embedding. Same kube-fetching
// contract: 5s timeout, init-container fallback, empty result on
// any error (the renderer + classifier both tolerate it).
func (w *Watcher) previousLogLines(p *corev1.Pod, reason string, n int) []string {
	if w == nil || w.Kube == nil || p == nil || n <= 0 {
		return nil
	}
	// ImagePullBackOff has no prior container — skip the log read.
	if reason == "ImagePullBackOff" || reason == "ErrImagePull" ||
		reason == "CreateContainerConfigError" || reason == "init:ImagePullBackOff" ||
		reason == "init:ErrImagePull" {
		return nil
	}
	// Find the first container with restart history. Main containers
	// take priority over init containers since runtime crashes are
	// what we usually want to surface.
	cName := ""
	for _, cs := range p.Status.ContainerStatuses {
		if cs.RestartCount > 0 {
			cName = cs.Name
			break
		}
	}
	if cName == "" {
		for _, cs := range p.Status.InitContainerStatuses {
			if cs.RestartCount > 0 {
				cName = cs.Name
				break
			}
		}
	}
	if cName == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tail := int64(n)
	prev := true
	req := w.Kube.Clientset.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
		Container: cName,
		Previous:  prev,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil
	}
	defer stream.Close()
	buf := make([]byte, 4096)
	var data []byte
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if rerr != nil {
			break
		}
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return nil
	}
	ls := strings.Split(s, "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return ls
}
