package projectmetrics

import (
	"context"
	"io"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// With metrics-server unreachable the sampler used to write a zero-usage
// row per project, dragging the cost rollup to 0 for the outage. It must
// write nothing. DB is nil here, so any insert attempt panics.
func TestSampleOnce_SkipsWhenMetricsServerMissing(t *testing.T) {
	cs := kubefake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "kuso", Labels: map[string]string{kube.LabelProject: "alpha"}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})
	s := &Sampler{Kube: &kube.Client{Clientset: cs}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := s.sampleOnce(context.Background()); err != nil {
		t.Fatalf("sampleOnce: %v", err)
	}
}
