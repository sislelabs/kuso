package releaserun

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// RUN-1: the release Job runs the env's image with its DB credentials, so it
// must get the env's egress (no internet for a privateEgress service; the
// platform API when asked for) and land on the env's pinned nodes.
func TestBuildJob_FollowsEnvEgressAndPlacement(t *testing.T) {
	t.Parallel()
	r := New(nil)
	env := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-api",
		PrivateEgress: true, PlatformAPIEgress: true,
		Placement: &kube.KusoPlacement{
			Labels: map[string]string{"region": "eu", "gpu": ""},
			Nodes:  []string{"node-a"},
		},
		Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}},
	}}
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "abc"}

	pod := r.buildJob(env, img, "j", 60).Spec.Template
	if _, ok := pod.Labels[kube.LabelEgressPublic]; ok {
		t.Errorf("privateEgress env's release pod got public egress: %v", pod.Labels)
	}
	if pod.Labels[kube.LabelEgressPlatformAPI] != "true" {
		t.Errorf("platformApiEgress env's release pod lacks the platform-api label: %v", pod.Labels)
	}
	if pod.Spec.NodeSelector["kuso.sislelabs.com/region"] != "eu" {
		t.Errorf("nodeSelector = %v, want region=eu", pod.Spec.NodeSelector)
	}
	if len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Value != "eu" {
		t.Errorf("tolerations = %v, want the region taint toleration", pod.Spec.Tolerations)
	}
	if pod.Spec.Affinity == nil {
		t.Fatalf("no affinity for a flag label + hostname pin")
	}
	exprs := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions
	want := []corev1.NodeSelectorRequirement{
		{Key: "kuso.sislelabs.com/gpu", Operator: corev1.NodeSelectorOpExists},
		{Key: "kubernetes.io/hostname", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-a"}},
	}
	if len(exprs) != len(want) {
		t.Fatalf("matchExpressions = %v, want %v", exprs, want)
	}
	for i := range want {
		if exprs[i].Key != want[i].Key || exprs[i].Operator != want[i].Operator {
			t.Errorf("matchExpressions[%d] = %v, want %v", i, exprs[i], want[i])
		}
	}

	env.Spec.PrivateEgress, env.Spec.PlatformAPIEgress, env.Spec.Placement = false, false, nil
	pod = r.buildJob(env, img, "j", 60).Spec.Template
	if pod.Labels[kube.LabelEgressPublic] != "true" {
		t.Errorf("default env's release pod lacks public egress: %v", pod.Labels)
	}
	if _, ok := pod.Labels[kube.LabelEgressPlatformAPI]; ok {
		t.Errorf("default env's release pod got platform-api egress: %v", pod.Labels)
	}
	if pod.Spec.NodeSelector != nil || pod.Spec.Affinity != nil || pod.Spec.Tolerations != nil {
		t.Errorf("unplaced env got scheduling constraints: %+v", pod.Spec)
	}
}
