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
		if st.Phase != "rebooting" {
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
		_ = w.setApplyState(ctx, n.Name, "done", "patched + rebooted; node back and uncordoned")
		if w.Notify != nil {
			w.Notify.Emit(notifyApplyDone(n.Name))
		}
		logger.Info("pkgupdates: node finished patch+reboot", "node", n.Name)
	}
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
	var backoff int32 = 1
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
