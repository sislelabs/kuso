// Package nodewatch detects node failures and reacts to them.
//
// Loop:
//   - Every Tick (default 1 min) list every cluster node.
//   - For each node track when its Ready condition last flipped.
//   - When NotReady persists past Threshold (default 5 min), cordon
//     the node and fire a notify "node.unreachable" event. Mark it
//     in our local set so we don't re-emit on every tick.
//   - When a previously-marked node transitions back to Ready,
//     uncordon (only if WE cordoned it) and fire "node.recovered".
//
// We never delete a node automatically — that's the operator's call
// from the /settings/nodes UI. Cordoning is reversible; deleting
// without explicit confirmation is the kind of thing that wakes
// people up at 3am.
package nodewatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

const (
	// CordonAnnotation marks nodes that nodewatch cordoned itself, so
	// recovery can safely uncordon (and not stomp on a manual cordon).
	CordonAnnotation = "kuso.sislelabs.com/cordoned-by-nodewatch"
)

// Config tunes the loop. Zero values fall back to defaults.
type Config struct {
	Tick      time.Duration
	Threshold time.Duration
}

func (c Config) tick() time.Duration {
	if c.Tick <= 0 {
		return 1 * time.Minute
	}
	return c.Tick
}

func (c Config) threshold() time.Duration {
	if c.Threshold <= 0 {
		return 5 * time.Minute
	}
	return c.Threshold
}

// Watcher polls Node conditions and fires events.
type Watcher struct {
	Kube   *kube.Client
	Notify *notify.Dispatcher
	Logger *slog.Logger
	Config Config

	mu sync.Mutex
	// notReadySince tracks the first observed NotReady moment per node.
	// Reset to zero when the node flips Ready. Used to compute
	// elapsed-NotReady and decide whether we've crossed the threshold.
	notReadySince map[string]time.Time
	// alerted records nodes we've already fired NodeUnreachable for so
	// we don't spam the webhook on every tick. Cleared when recovery
	// fires.
	alerted map[string]struct{}
}

// Run blocks until ctx is cancelled. Idempotent: re-running on the
// same cluster picks up state from existing node annotations.
func (w *Watcher) Run(ctx context.Context) {
	if w == nil || w.Kube == nil {
		return
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	if w.notReadySince == nil {
		w.notReadySince = map[string]time.Time{}
	}
	if w.alerted == nil {
		w.alerted = map[string]struct{}{}
	}
	w.Logger.Info("nodewatch starting",
		"tick", w.Config.tick(),
		"threshold", w.Config.threshold())
	t := time.NewTicker(w.Config.tick())
	defer t.Stop()
	// Tick once up-front so a freshly-restarted server reconciles
	// state without waiting a full minute.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			w.Logger.Info("nodewatch stopping")
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// pendingAction is a deferred kube/notify side effect produced by the
// state-machine pass under the lock. We collect them and dispatch
// after releasing w.mu so a slow kube apiserver can't pin the mutex
// for the full 15-second list-context timeout.
type pendingAction struct {
	kind string // "cordon" | "uncordon"
	node *corev1.Node
	emit notify.Event
}

func (w *Watcher) tick(ctx context.Context) {
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(listCtx, metav1.ListOptions{})
	if err != nil {
		w.Logger.Warn("nodewatch list nodes failed", "err", err)
		return
	}
	now := time.Now().UTC()

	// Pure state-machine pass under the lock — no kube calls, no
	// Notify.Emit. We mutate w.notReadySince and w.alerted, recording
	// any cordon/uncordon/notify actions to apply after unlocking.
	var actions []pendingAction
	w.mu.Lock()
	seen := map[string]struct{}{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		seen[n.Name] = struct{}{}
		ready := isReady(n)
		if ready {
			delete(w.notReadySince, n.Name)
			if _, was := w.alerted[n.Name]; was {
				delete(w.alerted, n.Name)
				actions = append(actions, pendingAction{
					kind: "uncordon", node: n, emit: notify.NodeRecovered(n.Name),
				})
			}
			continue
		}
		first, ok := w.notReadySince[n.Name]
		if !ok {
			w.notReadySince[n.Name] = now
			continue
		}
		if _, alreadyAlerted := w.alerted[n.Name]; alreadyAlerted {
			continue
		}
		if now.Sub(first) < w.Config.threshold() {
			continue
		}
		// We DO mark alerted optimistically here so subsequent ticks
		// don't pile up redundant cordon attempts during a slow kube
		// apiserver. The post-unlock dispatcher rolls back the alerted
		// flag on cordon failure to keep the original retry semantics.
		w.alerted[n.Name] = struct{}{}
		actions = append(actions, pendingAction{
			kind: "cordon", node: n, emit: notify.NodeUnreachable(n.Name, reasonFor(n)),
		})
	}
	for k := range w.notReadySince {
		if _, ok := seen[k]; !ok {
			delete(w.notReadySince, k)
		}
	}
	for k := range w.alerted {
		if _, ok := seen[k]; !ok {
			delete(w.alerted, k)
		}
	}
	w.mu.Unlock()

	// Dispatch deferred actions outside the lock. Any one of these can
	// block on a slow kube apiserver — that's fine now because nothing
	// else needs w.mu while we wait.
	for _, a := range actions {
		switch a.kind {
		case "cordon":
			if err := w.cordon(ctx, a.node); err != nil {
				w.Logger.Warn("nodewatch cordon", "node", a.node.Name, "err", err)
				// Roll back the optimistic alerted mark so the next
				// tick retries.
				w.mu.Lock()
				delete(w.alerted, a.node.Name)
				w.mu.Unlock()
				continue
			}
			w.Notify.Emit(a.emit)
		case "uncordon":
			if err := w.uncordonIfOurs(ctx, a.node); err != nil {
				w.Logger.Warn("nodewatch uncordon", "node", a.node.Name, "err", err)
			}
			w.Notify.Emit(a.emit)
		}
	}
}

// cordon patches spec.unschedulable=true and stamps our annotation so
// the recovery path can tell our cordon from a manual one.
func (w *Watcher) cordon(ctx context.Context, n *corev1.Node) error {
	if n.Spec.Unschedulable {
		// Already cordoned — still stamp the annotation so a later
		// recovery uncordons. Without this, manually-cordoned nodes
		// that go NotReady would never auto-recover even if they
		// flip Ready, which is the more confusing footgun.
		_ = w.annotate(ctx, n.Name, true)
		return nil
	}
	patch := []byte(fmt.Sprintf(
		`{"spec":{"unschedulable":true},"metadata":{"annotations":{%q:"true"}}}`,
		CordonAnnotation))
	_, err := w.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, n.Name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

// uncordonIfOurs only flips spec.unschedulable=false when we have our
// annotation on the node. If a human cordoned it for some other
// reason, we leave it alone.
//
// We re-fetch the node fresh (not the informer-cached `n`) before
// reading the annotation. The cache lags; under unlucky timing — a
// human running `kubectl annotate node X kuso.sislelabs.com/cordoned-
// by-nodewatch-` to claim ownership of a manual cordon, immediately
// followed by our recovery tick — the cached object would still show
// our annotation and we'd happily uncordon a node the operator
// expected to stay drained.
func (w *Watcher) uncordonIfOurs(ctx context.Context, n *corev1.Node) error {
	live, err := w.Kube.Clientset.CoreV1().Nodes().Get(ctx, n.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if live.Annotations[CordonAnnotation] != "true" {
		return nil
	}
	// Patch removes the annotation by setting it to null in JSON
	// merge-patch semantics.
	patch := []byte(fmt.Sprintf(
		`{"spec":{"unschedulable":false},"metadata":{"annotations":{%q:null}}}`,
		CordonAnnotation))
	_, err = w.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, n.Name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (w *Watcher) annotate(ctx context.Context, name string, on bool) error {
	val := "true"
	if !on {
		val = "false"
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, CordonAnnotation, val))
	_, err := w.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	return err
}

func isReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func reasonFor(n *corev1.Node) string {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue {
			if c.Message != "" {
				return c.Message
			}
			if c.Reason != "" {
				return c.Reason
			}
			return "node Ready=" + string(c.Status)
		}
	}
	return errors.New("no Ready condition reported").Error()
}
