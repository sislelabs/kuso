package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// cleanupPageLimit caps how many objects we fetch per list page. The
// previous code did one unbounded cluster-wide list per resource — on
// a cluster with 10k+ pods (busy CI clusters, weeks of accumulated
// build pods) that allocates hundreds of MB on the kube apiserver in
// one go. Paginating keeps memory per page bounded; the trade-off is
// O(pages) round-trips, which is fine for an admin-triggered sweep.
const cleanupPageLimit = 500

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

	// First pass: enumerate every Job page-by-page so we can both
	// (a) build the buildJobs lookup the pod loop needs, and
	// (b) keep the list of Job pointers for the second pass that
	// actually deletes them.
	//
	// Pre-pagination this was a single unbounded `Jobs("").List(...)` —
	// on a busy cluster with weeks of accumulated builds that's a
	// hundreds-of-MB alloc on the apiserver. The pageLimit keeps each
	// roundtrip bounded.
	buildJobs := map[string]bool{} // ns/name → true if owned by KusoBuild
	allJobs := make([]batchv1.Job, 0, cleanupPageLimit)
	if err := paginateJobs(ctx, h.Kube.Clientset, "", func(page []batchv1.Job) error {
		for i := range page {
			j := &page[i]
			for _, o := range j.OwnerReferences {
				if o.Kind == "KusoBuild" {
					buildJobs[j.Namespace+"/"+j.Name] = true
					break
				}
			}
			allJobs = append(allJobs, page[i])
		}
		return nil
	}); err != nil {
		http.Error(w, "list jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Pods: paginate, deleting eligible pods inline. Per-page memory
	// is bounded; we never hold more than cleanupPageLimit pod objects
	// at once. Server-side fieldSelectors don't support OR
	// (Succeeded || Failed), so we still filter client-side, but on
	// one page at a time.
	seenNS := map[string]struct{}{}
	if err := paginatePods(ctx, h.Kube.Clientset, "", func(page []corev1.Pod) error {
		for i := range page {
			p := &page[i]
			if skipNS[p.Namespace] {
				continue
			}
			if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
				continue
			}
			// Skip pods belonging to a KusoBuild's Job — see comment above.
			// The right way to clear stale builds is `kubectl delete kusobuild`,
			// not deleting the child Job/pod, which the operator just rebuilds.
			ownedByBuildJob := false
			for _, o := range p.OwnerReferences {
				if o.Kind == "Job" && buildJobs[p.Namespace+"/"+o.Name] {
					ownedByBuildJob = true
					break
				}
			}
			if ownedByBuildJob {
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
		return nil
	}); err != nil {
		http.Error(w, "list pods: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Jobs: completion-time-set is the safe signal. activeDeadline-
	// elapsed Jobs that haven't been marked Failed yet are left for
	// the kube garbage collector — touching them mid-state risks
	// racing with helm-operator's reconcile if a Job is parented to
	// a Helm release. We reuse the allJobs slice from the pre-pass.
	for i := range allJobs {
		j := &allJobs[i]
		if skipNS[j.Namespace] {
			continue
		}
		// Skip Jobs still actively running a pod.
		if j.Status.Active > 0 {
			continue
		}
		// Skip Jobs owned by a KusoBuild CRD. The operator's reconcile
		// will resurrect deleted Jobs from a stale KusoBuild — and on a
		// small box, a respawn cascade can OOM-thrash the host. Stale
		// KusoBuild CRDs themselves are the right thing to delete; this
		// handler doesn't touch CRDs.
		ownedByKusoBuild := false
		for _, o := range j.OwnerReferences {
			if o.Kind == "KusoBuild" {
				ownedByKusoBuild = true
				break
			}
		}
		if ownedByKusoBuild {
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

// paginatePods walks every page of pods in namespace (or all
// namespaces when ns == "") and calls cb with each page. The caller
// processes one page worth of pods at a time, so peak memory is
// bounded by cleanupPageLimit even on a cluster with millions of
// pods. Continue tokens are managed for the caller.
func paginatePods(ctx context.Context, cs kubernetes.Interface, ns string, cb func([]corev1.Pod) error) error {
	cont := ""
	for {
		page, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
			Limit:    cleanupPageLimit,
			Continue: cont,
		})
		if err != nil {
			return err
		}
		if err := cb(page.Items); err != nil {
			return err
		}
		cont = page.Continue
		if cont == "" {
			return nil
		}
	}
}

// paginateJobs is the batch/v1 counterpart of paginatePods.
func paginateJobs(ctx context.Context, cs kubernetes.Interface, ns string, cb func([]batchv1.Job) error) error {
	cont := ""
	for {
		page, err := cs.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
			Limit:    cleanupPageLimit,
			Continue: cont,
		})
		if err != nil {
			return err
		}
		if err := cb(page.Items); err != nil {
			return err
		}
		cont = page.Continue
		if cont == "" {
			return nil
		}
	}
}
