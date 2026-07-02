package pkgupdates

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Apply errors mapped by the HTTP layer.
var (
	// ErrNothingToDo: the node reports no applicable updates.
	ErrNothingToDo = errors.New("pkgupdates: nothing to apply")
	// ErrAlreadyRunning: an apply Job for this node is still in flight.
	ErrAlreadyRunning = errors.New("pkgupdates: apply already in progress")
)

// ApplyStateAnnotation tracks an in-progress / completed apply on a node
// so the UI can poll status even across a reboot that takes kuso-server
// down (single-node case). Value is JSON: {phase, at, log}.
const ApplyStateAnnotation = "kuso.sislelabs.com/pkg-apply-state"

// CordonAnnotation marks a cordon WE applied for a patch+reboot, so the
// rejoin reconcile only uncordons nodes we cordoned (never one an
// operator or nodewatch cordoned for another reason).
const CordonAnnotation = "kuso.sislelabs.com/cordoned-by-pkgupdates"

// applyJobName is deterministic per node so the concurrency lock is just
// "does this Job already exist". Node names are RFC-1123 already.
func applyJobName(node string) string { return "kuso-pkg-apply-" + node }

// Apply launches the per-node patch Job. allowReboot gates the reboot
// branch inside the Job: when false, the Job patches and stops even if a
// reboot is required (setting rebootRequired in the apply-state); when
// true, the Job runs the cordon/drain/reboot orchestration (phase 4).
//
// Concurrency: a deterministic Job name means a second Apply while one
// is running returns ErrAlreadyRunning rather than stacking Jobs.
func (w *Watcher) Apply(ctx context.Context, node string, allowReboot bool) error {
	if w.Kube == nil {
		return fmt.Errorf("pkgupdates: no kube client")
	}
	// Node must exist + report a supported pkg manager with updates,
	// else there's nothing to do (and we avoid launching a Job that
	// would no-op or run against an unknown OS).
	live, err := w.Kube.Clientset.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return err
	}
	adv := ParseAnnotation(node, live.Annotations[Annotation])
	if !adv.HasUpdates() {
		return fmt.Errorf("%w: node %s has no applicable updates (pkgMgr=%q count=%d)",
			ErrNothingToDo, node, adv.PkgMgr, adv.Count)
	}

	// Serialize reboot-applies across nodes: never drain+reboot two nodes
	// at once, or a multi-node cluster could lose a quorum/all replicas of
	// a service simultaneously. If ANOTHER node is already mid patch+reboot
	// (carrying our cordon marker, or apply-state draining/rebooting),
	// refuse — the operator applies nodes one at a time. A non-reboot apply
	// (allowReboot=false) doesn't evict or reboot, so it isn't gated.
	if allowReboot {
		if busy, err := w.anotherNodeRebooting(ctx, node); err != nil {
			return fmt.Errorf("check other-node apply: %w", err)
		} else if busy != "" {
			return fmt.Errorf("%w: node %s is still applying host updates (drain+reboot in progress); apply nodes one at a time", ErrAlreadyRunning, busy)
		}
	}

	// Concurrency lock: refuse if a prior apply Job is still around.
	name := applyJobName(node)
	if existing, gerr := w.Kube.Clientset.BatchV1().Jobs(w.namespace()).Get(ctx, name, metav1.GetOptions{}); gerr == nil {
		if existing.Status.Succeeded == 0 && existing.Status.Failed == 0 {
			return ErrAlreadyRunning
		}
		// A finished Job from a previous apply — clear it so we can
		// re-launch cleanly.
		bg := metav1.DeletePropagationBackground
		_ = w.Kube.Clientset.BatchV1().Jobs(w.namespace()).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
	}

	// Reset apply-state to running before launching.
	if err := w.setApplyState(ctx, node, "running", ""); err != nil {
		return fmt.Errorf("set apply-state: %w", err)
	}

	// When a reboot is allowed, cordon up front (stamping our ownership
	// marker) so scheduling stops the instant apply begins — don't wait
	// for the Job to reach its reboot branch. The rejoin reconcile
	// uncordons (only our marker) once the node is Ready again. If the
	// patch turns out not to need a reboot, the Job leaves the node
	// cordoned only briefly; we uncordon on a non-reboot 'done' too.
	if allowReboot {
		patch := fmt.Sprintf(
			`{"spec":{"unschedulable":true},"metadata":{"annotations":{%q:"true"}}}`,
			CordonAnnotation)
		if _, err := w.Kube.Clientset.CoreV1().Nodes().Patch(
			ctx, node, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("cordon before apply: %w", err)
		}
	}

	job := w.buildApplyJob(node, adv.PkgMgr, allowReboot)
	if _, err := w.Kube.Clientset.BatchV1().Jobs(w.namespace()).Create(ctx, job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create apply job: %w", err)
	}
	return nil
}

// namespace returns the home namespace (kuso) for the apply Job. The
// Watcher doesn't carry a namespace field today; the apply Job lives in
// the control-plane namespace regardless of the target node.
func (w *Watcher) namespace() string { return "kuso" }

// anotherNodeRebooting reports the name of any node OTHER than `except`
// that is currently mid patch+reboot: it carries our cordon marker, or
// its apply-state phase is draining/rebooting. Returns "" when no other
// node is busy. This is what enforces one-node-at-a-time draining: the
// signal is durable on the Node object, so it holds even across a
// kuso-server restart mid-operation.
func (w *Watcher) anotherNodeRebooting(ctx context.Context, except string) (string, error) {
	nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Name == except {
			continue
		}
		if n.Annotations[CordonAnnotation] == "true" {
			return n.Name, nil
		}
		switch parseApplyState(n.Annotations[ApplyStateAnnotation]).Phase {
		case "draining", "rebooting", "running":
			return n.Name, nil
		}
	}
	return "", nil
}

// reconcileReboots finalizes nodes that finished a patch+reboot. A node
// whose apply-state is "rebooting" and is now Ready again is uncordoned
// (only if WE own the cordon marker — never an operator's cordon), its
// apply-state flips to "done", and a notify fires. Called each watcher
// tick; cheap (only acts on rebooting nodes). This is how the operation
// survives the single-node self-reboot: the Job pod died with the node,
// but the annotations persisted and the freshly-booted kuso-server
// picks the finalization back up here.
func (w *Watcher) reconcileReboots(ctx context.Context, logger *slog.Logger) {
	nodes, err := w.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		st := parseApplyState(n.Annotations[ApplyStateAnnotation])
		// Act on a node mid-reboot (phase=rebooting), still settling after
		// the reboot (phase=settling — see below), OR any node still
		// carrying OUR cordon marker (defense in depth: if a post-reboot
		// race overwrote the apply-state, the marker is the durable signal
		// that we cordoned this node for a patch and owe it an uncordon).
		// Skip nodes we don't own.
		ours := n.Annotations[CordonAnnotation] == "true"
		if st.Phase != "rebooting" && st.Phase != "settling" && !ours {
			continue
		}
		if !nodeReady(n) {
			continue // still down / not back yet
		}
		// Back up. Uncordon iff our marker is set.
		if n.Annotations[CordonAnnotation] == "true" {
			patch := fmt.Sprintf(
				`{"spec":{"unschedulable":false},"metadata":{"annotations":{%q:null}}}`,
				CordonAnnotation)
			if _, err := w.Kube.Clientset.CoreV1().Nodes().Patch(
				ctx, n.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
				logger.Warn("pkgupdates: uncordon after reboot", "node", n.Name, "err", err)
				continue
			}
		}
		// Reschedule pods the reboot stranded. When a node reboots, kubelet
		// stops reporting and its pods go into an Unknown phase; a
		// StatefulSet/Deployment will NOT recreate an Unknown pod because
		// the control plane can't confirm the old one is gone. Left alone,
		// a singleton like the instance pg-pooler stays down until manual
		// intervention (observed: cluster-wide DB outage on a worker
		// reboot). Force-delete them so their controllers reschedule.
		//
		// A pod can flip to Unknown a little AFTER the node returns Ready
		// (the kubelet re-registers, then the control plane times out the
		// stale pod). So a single sweep at finalize can miss it — we keep
		// the node in a `settling` phase and sweep every tick until either
		// no Unknown pods remain (→ done) or a bounded window elapses. This
		// keeps the node in the reconcile set instead of dropping straight
		// to `done`, which was the gap that stranded distill-db-pooler.
		swept := w.rescheduleStrandedPods(ctx, n.Name, logger)

		// Settle window: how long after the node returns we keep watching
		// for late-appearing Unknown pods before declaring done. Measured
		// from the current apply-state timestamp (the reboot stamp on the
		// first pass, then the settling stamp). A malformed/absent stamp
		// parses to zero time → treated as "window elapsed" so we never
		// wedge in settling forever on a parse error.
		const settleWindow = 3 * time.Minute
		at, perr := time.Parse(time.RFC3339, st.At)
		settledOut := perr != nil || time.Since(at) > settleWindow

		if swept > 0 || !settledOut {
			// Still cleaning up or inside the settle window — hold in
			// `settling` so the next tick re-sweeps. Only stamp the
			// transition once (when we first arrive here from rebooting)
			// so we don't churn the annotation every 15s.
			if st.Phase != "settling" {
				_ = w.setApplyState(ctx, n.Name, "settling", "node back + uncordoned; watching for stranded pods")
				logger.Info("pkgupdates: node back, settling", "node", n.Name)
			}
			continue
		}

		_ = w.setApplyState(ctx, n.Name, "done", "patched + rebooted; node back and uncordoned")
		if w.Notify != nil {
			w.Notify.Emit(notifyApplyDone(n.Name))
		}
		logger.Info("pkgupdates: node finished patch+reboot", "node", n.Name)
	}
}

// rescheduleStrandedPods force-deletes pods on `node` that the reboot
// left in an Unknown phase (kubelet lost then regained contact). A
// controller won't recreate an Unknown pod on its own — the API server
// can't confirm the container is actually gone — so we delete with
// grace-period 0 to let the ReplicaSet/StatefulSet spawn a replacement
// on a healthy node. Best-effort: a failure here is logged, not fatal,
// and the next finalize tick retries because the pod is still Unknown.
//
// Scope is deliberately narrow — only pods on THIS node in Unknown phase.
// We do not touch Running/Pending pods (the kubelet re-adopts those after
// reboot) or pods on other nodes.
// Returns the number of stranded pods it force-deleted this pass (0 when
// the node is clean), so the caller can decide whether to keep settling.
func (w *Watcher) rescheduleStrandedPods(ctx context.Context, node string, logger *slog.Logger) int {
	pods, err := w.Kube.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		logger.Warn("pkgupdates: list pods for stranded-pod sweep", "node", node, "err", err)
		return 0
	}
	zero := int64(0)
	swept := 0
	for i := range pods.Items {
		p := &pods.Items[i]
		// PodPhase Unknown = kubelet cannot report the pod's state
		// (node was unreachable). This is the reboot-stranded signature.
		if p.Status.Phase != corev1.PodUnknown {
			continue
		}
		if err := w.Kube.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{
			GracePeriodSeconds: &zero,
		}); err != nil && !apierrors.IsNotFound(err) {
			logger.Warn("pkgupdates: force-delete stranded pod",
				"node", node, "pod", p.Namespace+"/"+p.Name, "err", err)
			continue
		}
		swept++
		logger.Info("pkgupdates: rescheduled stranded pod after reboot",
			"node", node, "pod", p.Namespace+"/"+p.Name)
	}
	return swept
}

// nodeReady reports whether the node's Ready condition is True.
func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// setApplyState merge-patches the apply-state annotation onto the node.
func (w *Watcher) setApplyState(ctx context.Context, node, phase, logTail string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	// Build the inner JSON, then escape it for the merge-patch value.
	inner := fmt.Sprintf(`{"phase":%q,"at":%q,"log":%q}`, phase, now, truncate(logTail, 2000))
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, ApplyStateAnnotation, inner)
	_, err := w.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, node, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:] // keep the TAIL (where errors land)
}

// buildApplyJob renders the privileged per-node Job that nsenters the
// host and runs the upgrade. The reboot branch is gated by allowReboot.
func (w *Watcher) buildApplyJob(node, pkgMgr string, allowReboot bool) *batchv1.Job {
	privileged := true
	hostPID := true
	var ttl int32 = 3600
	// BackoffLimit MUST be 0: the reboot branch kills this pod when the
	// node goes down, which the Job controller would otherwise read as a
	// failure and RE-RUN the Job on the rebooted node. That re-run finds
	// nothing left to upgrade, overwrites apply-state rebooting→done, and
	// defeats the reconcileReboots uncordon (the node stays cordoned).
	// The reboot IS the success; never retry.
	var backoff int32 = 0
	rebootFlag := "false"
	if allowReboot {
		rebootFlag = "true"
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applyJobName(node),
			Namespace: w.namespace(),
			Labels: map[string]string{
				"app.kubernetes.io/name":            "kuso-pkg-apply",
				"kuso.sislelabs.com/pkg-apply-node": node,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app.kubernetes.io/name": "kuso-pkg-apply"},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					NodeName:           node, // pin to the target node
					HostPID:            hostPID,
					ServiceAccountName: "kuso-pkg-probe", // reuses the probe SA (nodes get/patch)
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
					Containers: []corev1.Container{{
						Name:    "apply",
						Image:   "alpine:3.20",
						Command: []string{"/bin/sh", "-c", applyScript},
						// KUBERNETES_SERVICE_HOST/PORT are injected by kube
						// automatically; the script uses them for the API
						// patch (same SA-token path as the probe).
						Env: []corev1.EnvVar{
							{Name: "NODE_NAME", Value: node},
							{Name: "PKG_MGR", Value: pkgMgr},
							{Name: "ALLOW_REBOOT", Value: rebootFlag},
							{Name: "APPLY_ANNOTATION", Value: ApplyStateAnnotation},
							{Name: "CORDON_ANNOTATION", Value: CordonAnnotation},
						},
						SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
					}},
				},
			},
		},
	}
}
