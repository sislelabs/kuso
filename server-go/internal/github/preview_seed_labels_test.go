package github

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The project netpols select pods by kuso.sislelabs.com/project. An
// unlabelled seed pod is denied ingress at the (project-labelled) preview
// DB in any namespace carrying the policies, so the seed never lands.
// Public egress mirrors the release/migrate Jobs, which run the same
// user image.
func TestRunPreviewSeedJob_PodTemplateCarriesProjectLabel(t *testing.T) {
	d := newDispatcher(t)
	if err := d.runPreviewSeedJob(context.Background(), "e2e", "e2e-web-pr-1", "preview-pr-1", "img:tag", "npm run seed", []string{"e2e-db-pr-1-conn", "e2e-web-pr-1-secrets"}, nil, false, false); err != nil {
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
	// ISO-3: the preview's env scope (what its env pods carry, so the
	// per-env policy admits it) and its clone DB subscription.
	if got := labels["kuso.sislelabs.com/env"]; got != "preview-pr-1" {
		t.Errorf("pod template env = %q, want preview-pr-1", got)
	}
	if got := labels["conn.kuso.sislelabs.com/6a1a8e2c8ce5027f4d4d"]; got != "true" {
		t.Errorf("pod template lacks the clone DB subscriber label: %v", labels)
	}

	// ISO-6: the seed runs PR code, so it gets the env pods' sandbox.
	spec := jobs.Items[0].Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Errorf("seed pod automountServiceAccountToken not false")
	}
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Errorf("seed pod enableServiceLinks not false")
	}
	if spec.SecurityContext == nil || spec.SecurityContext.SeccompProfile == nil ||
		spec.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("seed pod seccomp not RuntimeDefault")
	}
	sc := spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("seed container securityContext = %+v, want no privesc + drop ALL", sc)
	}
}

// ISO-7: the seed runs the PR's code with the preview's DB credentials, so
// it gets the service's egress, not unconditional internet access.
func TestSeedPodLabels_FollowServiceEgress(t *testing.T) {
	priv := seedPodLabels("e2e", "e2e-web-pr-1", "preview-pr-1", nil, nil, true, true)
	if _, ok := priv["kuso.sislelabs.com/network-egress-public"]; ok {
		t.Errorf("privateEgress seed pod got public egress: %v", priv)
	}
	if priv["kuso.sislelabs.com/network-egress-platform-api"] != "true" {
		t.Errorf("platformApiEgress seed pod lacks the platform-api label: %v", priv)
	}
	pub := seedPodLabels("e2e", "e2e-web-pr-1", "preview-pr-1", nil, nil, false, false)
	if pub["kuso.sislelabs.com/network-egress-public"] != "true" {
		t.Errorf("default seed pod lacks public egress: %v", pub)
	}
	if _, ok := pub["kuso.sislelabs.com/network-egress-platform-api"]; ok {
		t.Errorf("default seed pod got platform-api egress: %v", pub)
	}
}
