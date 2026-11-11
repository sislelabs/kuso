package handlers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func topPod(name string, phase corev1.PodPhase, lbls map[string]string, c corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: lbls},
		Spec:       corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{c}},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: []corev1.ContainerStatus{{Name: c.Name, RestartCount: 3}},
		},
	}
}

func TestBuildProjectTopRows(t *testing.T) {
	app := corev1.Container{Name: "app", Resources: corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
	}}
	pods := []*corev1.Pod{
		topPod("web-1", corev1.PodRunning, map[string]string{"app.kubernetes.io/instance": "p-web-production"}, app),
		topPod("db-0", corev1.PodRunning, map[string]string{labelAddon: "p-db", "app.kubernetes.io/instance": "p-db"}, corev1.Container{Name: "postgres"}),
		topPod("build-x", corev1.PodSucceeded, nil, app),
	}
	metrics := []unstructured.Unstructured{*podMetric("web-1", "p-web-production", "20000000n", "100Mi")}
	db := podMetric("db-0", "p-db", "5m", "300Mi")
	db.Object["containers"].([]any)[0].(map[string]any)["name"] = "postgres"
	metrics = append(metrics, *db)

	rows := buildProjectTopRows(pods, metrics)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (succeeded pod dropped): %+v", len(rows), rows)
	}
	if rows[0].Pod != "db-0" || rows[0].Workload != "p-db" || rows[0].MemBytes != 300<<20 {
		t.Errorf("row 0 = %+v, want db-0 (highest mem) keyed by addon label", rows[0])
	}
	got := rows[1]
	want := projectTopRow{
		Workload: "p-web-production", Pod: "web-1", Container: "app", Node: "node-a", Phase: "Running",
		HasUsage: true, CPUm: 20, CPURequestm: 250, MemBytes: 100 << 20,
		MemRequest: 512 << 20, MemLimit: 1 << 30, Restarts: 3,
	}
	if got != want {
		t.Errorf("web row\n got %+v\nwant %+v", got, want)
	}
}
