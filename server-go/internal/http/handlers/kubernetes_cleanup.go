package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CleanupResponse summarises what the cleanup pass did. Counters per
// category so the UI can render a "deleted N pods + M jobs" toast +
// surface them in a more detailed log if a future iteration wants
// per-namespace drill-down.
type CleanupResponse struct {
	PodsDeleted      int      `json:"podsDeleted"`
	JobsDeleted      int      `json:"jobsDeleted"`
	Namespaces       []string `json:"namespaces"`
	Errors           []string `json:"errors,omitempty"`
}

// CleanupCompleted deletes finished pods (Succeeded / Failed phases)
// and finished Jobs across all namespaces. The host accumulates
// these — kuso-update Job pods, addon backup CronJob children,
// kaniko build pods — and even though they don't run code, they
// still hold cgroup state until containerd reaps them. On a
// 2-vCPU box, "47 stale pods" is enough to push readiness probes
// past their threshold and start a cascade.
//
// Safety:
//   - Only Succeeded / Failed pods are touched. Running pods are
//     never deleted by this handler regardless of namespace.
//   - Jobs are only deleted when status.completionTime != nil
//     (Complete) OR all spec.activeDeadlineSeconds have elapsed
//     with type=Failed in conditions. Active Jobs (still running
//     a pod) are left alone.
//   - kuso-system namespaces (kube-system, kuso-operator-system,
//     kube-node-lease, kube-public) are skipped to avoid surprises
//     — those don't usually have stale pods, but if they do,
//     deletion is the cluster operator's call.
func (h *KubernetesHandler) CleanupCompleted(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	out := CleanupResponse{Namespaces: []string{}}
	skipNS := map[string]bool{
		"kube-system":          true,
		"kube-node-lease":      true,
		"kube-public":          true,
		"kuso-operator-system": true,
	}

	bg := metav1.DeletePropagationBackground

	// Pods: pull the cluster-wide list once, filter client-side. Server-
	// side fieldSelectors don't support OR (Succeeded || Failed), so
	// the server does two list calls or filters locally. Local is
	// cheaper for a single sweep.
	pods, err := h.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		http.Error(w, "list pods: "+err.Error(), http.StatusInternalServerError)
		return
	}
	seenNS := map[string]struct{}{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if skipNS[p.Namespace] {
			continue
		}
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			continue
		}
		if err := h.Kube.Clientset.CoreV1().Pods(p.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{
			PropagationPolicy: &bg,
		}); err != nil && !apierrors.IsNotFound(err) {
			out.Errors = append(out.Errors, "pod "+p.Namespace+"/"+p.Name+": "+err.Error())
			continue
		}
		out.PodsDeleted++
		seenNS[p.Namespace] = struct{}{}
	}

	// Jobs: completion-time-set is the safe signal. activeDeadline-
	// elapsed Jobs that haven't been marked Failed yet are left for
	// the kube garbage collector — touching them mid-state risks
	// racing with helm-operator's reconcile if a Job is parented to
	// a Helm release.
	jobs, err := h.Kube.Clientset.BatchV1().Jobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		http.Error(w, "list jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range jobs.Items {
		j := &jobs.Items[i]
		if skipNS[j.Namespace] {
			continue
		}
		// Skip Jobs still actively running a pod.
		if j.Status.Active > 0 {
			continue
		}
		// Need either CompletionTime (success) or a "Failed" condition
		// with reason BackoffLimitExceeded / DeadlineExceeded.
		complete := j.Status.CompletionTime != nil
		failed := false
		for _, c := range j.Status.Conditions {
			if c.Type == "Failed" && c.Status == corev1.ConditionTrue {
				failed = true
				break
			}
		}
		if !complete && !failed {
			continue
		}
		if err := h.Kube.Clientset.BatchV1().Jobs(j.Namespace).Delete(ctx, j.Name, metav1.DeleteOptions{
			PropagationPolicy: &bg,
		}); err != nil && !apierrors.IsNotFound(err) {
			out.Errors = append(out.Errors, "job "+j.Namespace+"/"+j.Name+": "+err.Error())
			continue
		}
		out.JobsDeleted++
		seenNS[j.Namespace] = struct{}{}
	}

	for ns := range seenNS {
		out.Namespaces = append(out.Namespaces, ns)
	}
	// Stable order so the response is reproducible across reloads —
	// nice for the toast that lists namespaces touched.
	sortedNamespaces := append([]string(nil), out.Namespaces...)
	for i := 0; i < len(sortedNamespaces); i++ {
		for j := i + 1; j < len(sortedNamespaces); j++ {
			if strings.Compare(sortedNamespaces[i], sortedNamespaces[j]) > 0 {
				sortedNamespaces[i], sortedNamespaces[j] = sortedNamespaces[j], sortedNamespaces[i]
			}
		}
	}
	out.Namespaces = sortedNamespaces

	writeJSON(w, http.StatusOK, out)
}
