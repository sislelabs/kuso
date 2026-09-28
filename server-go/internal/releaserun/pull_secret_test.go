package releaserun

import (
	"testing"

	"kuso/server/internal/kube"
)

// A runtime=image service on a private registry runs its release hook
// from that same private image, so the Job needs the pull secret too.
func TestBuildJob_ImagePullSecret(t *testing.T) {
	t.Parallel()
	r := New(nil)
	env := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-api",
		Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}},
	}}

	job := r.buildJob(env, &kube.KusoImage{Repository: "ghcr.io/acme/api", Tag: "v1", PullSecret: "alpha-regcred-ghcr-io"}, "j", 60)
	got := job.Spec.Template.Spec.ImagePullSecrets
	if len(got) != 1 || got[0].Name != "alpha-regcred-ghcr-io" {
		t.Errorf("imagePullSecrets = %+v", got)
	}

	job = r.buildJob(env, &kube.KusoImage{Repository: "registry/alpha/api", Tag: "v1"}, "j", 60)
	if n := len(job.Spec.Template.Spec.ImagePullSecrets); n != 0 {
		t.Errorf("no pull secret on the image must render none, got %d", n)
	}
}
