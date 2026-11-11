package handlers

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

const labelAddon = "kuso.sislelabs.com/addon"

// projectTopRow is one container: live usage from metrics-server next
// to the requests/limits from the pod spec, for spotting over- or
// under-provisioned workloads. HasUsage is false when metrics-server
// has no sample for the container yet.
type projectTopRow struct {
	Workload    string `json:"workload"`
	Pod         string `json:"pod"`
	Container   string `json:"container"`
	Node        string `json:"node"`
	Phase       string `json:"phase"`
	HasUsage    bool   `json:"hasUsage"`
	CPUm        int64  `json:"cpuMillicores"`
	CPURequestm int64  `json:"cpuRequestMillicores"`
	CPULimitm   int64  `json:"cpuLimitMillicores"`
	MemBytes    int64  `json:"memBytes"`
	MemRequest  int64  `json:"memRequestBytes"`
	MemLimit    int64  `json:"memLimitBytes"`
	Restarts    int32  `json:"restarts"`
}

type projectTopResponse struct {
	Project   string          `json:"project"`
	Namespace string          `json:"namespace"`
	Metrics   bool            `json:"metricsAvailable"`
	Rows      []projectTopRow `json:"rows"`
}

// ProjectTop is GET /api/projects/{project}/top: per-container usage vs
// requests/limits for every live pod in the project (envs, addons,
// builds, crons), sorted by memory used.
func (h *KubernetesHandler) ProjectTop(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	ctx, cancel := kubeCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out := projectTopResponse{Project: project, Rows: []projectTopRow{}}
	if h.Kube == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ns := h.projectNamespace(ctx, project)
	out.Namespace = ns
	pods, err := h.projectPodsInNS(ctx, ns, project)
	if err != nil {
		h.Logger.Error("project top: list pods", "project", project, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	items, ok := listPodMetricsCached(ctx, h.Kube, ns)
	out.Metrics = ok
	out.Rows = buildProjectTopRows(pods, items)
	writeJSON(w, http.StatusOK, out)
}

func (h *KubernetesHandler) projectPodsInNS(ctx context.Context, ns, project string) ([]*corev1.Pod, error) {
	sel := labels.SelectorFromSet(labels.Set{kube.LabelProject: project})
	if pods, ok := h.Kube.Cache.ListPodsByLabel(sel); ok {
		out := make([]*corev1.Pod, 0, len(pods))
		for _, p := range pods {
			if p.Namespace == ns {
				out = append(out, p)
			}
		}
		return out, nil
	}
	if h.Kube.Clientset == nil {
		return nil, fmt.Errorf("kube clientset not wired")
	}
	list, err := h.Kube.Clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	out := make([]*corev1.Pod, len(list.Items))
	for i := range list.Items {
		out[i] = &list.Items[i]
	}
	return out, nil
}

type containerUsage struct{ cpuMilli, memBytes int64 }

// buildProjectTopRows joins pod specs with PodMetrics by
// (pod, container). Finished pods (Succeeded/Failed) are dropped: they
// hold no resources and would bury the live rows.
func buildProjectTopRows(pods []*corev1.Pod, metrics []unstructured.Unstructured) []projectTopRow {
	usage := map[string]containerUsage{}
	for i := range metrics {
		containers, _ := metrics[i].Object["containers"].([]any)
		for _, c := range containers {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			name, _ := cm["name"].(string)
			u, _ := cm["usage"].(map[string]any)
			cpu, _ := u["cpu"].(string)
			mem, _ := u["memory"].(string)
			usage[metrics[i].GetName()+"/"+name] = containerUsage{parseCPU(cpu), parseQuantity(mem)}
		}
	}
	rows := []projectTopRow{}
	for _, p := range pods {
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		workload := p.Labels[labelAddon]
		if workload == "" {
			workload = p.Labels["app.kubernetes.io/instance"]
		}
		restarts := map[string]int32{}
		for _, cs := range p.Status.ContainerStatuses {
			restarts[cs.Name] = cs.RestartCount
		}
		for _, c := range p.Spec.Containers {
			row := projectTopRow{
				Workload:    workload,
				Pod:         p.Name,
				Container:   c.Name,
				Node:        p.Spec.NodeName,
				Phase:       string(p.Status.Phase),
				CPURequestm: c.Resources.Requests.Cpu().MilliValue(),
				CPULimitm:   c.Resources.Limits.Cpu().MilliValue(),
				MemRequest:  c.Resources.Requests.Memory().Value(),
				MemLimit:    c.Resources.Limits.Memory().Value(),
				Restarts:    restarts[c.Name],
			}
			if u, ok := usage[p.Name+"/"+c.Name]; ok {
				row.HasUsage = true
				row.CPUm = u.cpuMilli
				row.MemBytes = u.memBytes
			}
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].MemBytes != rows[j].MemBytes {
			return rows[i].MemBytes > rows[j].MemBytes
		}
		return rows[i].Pod+"/"+rows[i].Container < rows[j].Pod+"/"+rows[j].Container
	})
	return rows
}
