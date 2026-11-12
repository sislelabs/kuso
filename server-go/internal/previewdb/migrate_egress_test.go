package previewdb

import (
	"testing"

	"kuso/server/internal/kube"
)

// ISO-7 / RUN-1: the post-seed migrate runs the PR's code with the clone's
// credentials, so it gets the env's egress and lands on the env's nodes.
func TestBuildMigrateJob_FollowsEnvEgressAndPlacement(t *testing.T) {
	t.Parallel()
	env := previewEnvCR("tickero-api-pr-36", []string{"tickero-db-pr-36-conn"}, []string{"migrate"}, "abc")
	env.Spec.PrivateEgress = true
	env.Spec.PlatformAPIEgress = true
	env.Spec.Placement = &kube.KusoPlacement{Labels: map[string]string{"tier": "db"}}

	pod := buildMigrateJob("kuso", "tickero", "tickero-db-pr-36", &env, "", 1).Spec.Template
	if _, ok := pod.Labels[kube.LabelEgressPublic]; ok {
		t.Errorf("privateEgress env's migrate pod got public egress: %v", pod.Labels)
	}
	if pod.Labels[kube.LabelEgressPlatformAPI] != "true" {
		t.Errorf("platformApiEgress env's migrate pod lacks the platform-api label: %v", pod.Labels)
	}
	if pod.Spec.NodeSelector["kuso.sislelabs.com/tier"] != "db" {
		t.Errorf("migrate pod nodeSelector = %v, want the env's placement", pod.Spec.NodeSelector)
	}

	env.Spec.PrivateEgress, env.Spec.PlatformAPIEgress = false, false
	pod = buildMigrateJob("kuso", "tickero", "tickero-db-pr-36", &env, "", 1).Spec.Template
	if pod.Labels[kube.LabelEgressPublic] != "true" {
		t.Errorf("default env's migrate pod lacks public egress: %v", pod.Labels)
	}
}
