package nodeshape

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildSummaries_ReportsPressureConditions(t *testing.T) {
	n := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	n.Status.Conditions = []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue, Reason: "KubeletHasDiskPressure", Message: "kubelet has disk pressure"},
		{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
	}
	got := BuildSummaries([]corev1.Node{n}, nil, nil)[0].Conditions
	if len(got) != 2 || got[0].Type != "MemoryPressure" || got[1].Type != "DiskPressure" || got[1].Status != "True" || got[1].Reason != "KubeletHasDiskPressure" {
		t.Fatalf("conditions = %+v", got)
	}
}
