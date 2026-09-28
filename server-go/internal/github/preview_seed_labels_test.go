package github

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The project netpols select pods by kuso.sislelabs.com/project. An
// unlabelled seed pod is denied ingress at the (project-labelled) preview
// DB in any namespace carrying the policies, so the seed never lands.
// Public egress mirrors the release/migrate Jobs, which run the same
// user image.
func TestRunPreviewSeedJob_PodTemplateCarriesProjectLabel(t *testing.T) {
	d := newDispatcher(t)
	if err := d.runPreviewSeedJob(context.Background(), "e2e", "e2e-web-pr-1", "img:tag", "npm run seed", nil, nil); err != nil {
		t.Fatalf("runPreviewSeedJob: %v", err)
	}
	jobs, err := d.Kube.Clientset.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("list jobs: %v (n=%d)", err, len(jobs.Items))
	}
	labels := jobs.Items[0].Spec.Template.Labels
	if got := labels["kuso.sislelabs.com/project"]; got != "e2e" {
		t.Errorf("pod template kuso.sislelabs.com/project = %q, want e2e", got)
	}
	if got := labels["kuso.sislelabs.com/network-egress-public"]; got != "true" {
		t.Errorf("pod template network-egress-public = %q, want true", got)
	}
}
