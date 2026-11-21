package releaserun

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// OPS-1: a gosu/setpriv image whose web pods run with the service's
// opt-in capabilities exited 127 in its release hook, which ran with
// every capability dropped.
func TestBuildJob_AppliesEnvSecurityContext(t *testing.T) {
	t.Parallel()
	r := New(nil)
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "v1"}
	yes := true
	env := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-api",
		Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}},
		SecurityContext: &kube.KusoSecurityContext{
			AllowPrivilegeEscalation: &yes,
			Capabilities:             &kube.KusoCapabilities{Add: []string{"SETUID", "SETGID"}},
		},
	}}
	sc := r.buildJob(env, img, "j", 60).Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || !*sc.AllowPrivilegeEscalation {
		t.Fatalf("release container allowPrivilegeEscalation = %v, want true", sc)
	}
	if !slices.Equal(sc.Capabilities.Add, []corev1.Capability{"SETUID", "SETGID"}) ||
		!slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatalf("release container capabilities = %+v, want drop ALL + add SETUID,SETGID", sc.Capabilities)
	}

	env.Spec.SecurityContext = nil
	sc = r.buildJob(env, img, "j", 60).Spec.Template.Spec.Containers[0].SecurityContext
	if *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Add) != 0 {
		t.Fatalf("default release container context relaxed: %+v", sc)
	}
}

func TestBuildJob_HostUsersFollowsEnv(t *testing.T) {
	t.Parallel()
	r := New(nil)
	img := &kube.KusoImage{Repository: "registry/alpha/api", Tag: "v1"}
	f := false
	env := &kube.KusoEnvironment{Spec: kube.KusoEnvironmentSpec{
		Project: "alpha", Service: "alpha-api",
		Release:         &kube.KusoReleaseSpec{Command: []string{"migrate"}},
		SecurityContext: &kube.KusoSecurityContext{HostUsers: &f},
	}}
	if hu := r.buildJob(env, img, "j", 60).Spec.Template.Spec.HostUsers; hu == nil || *hu {
		t.Fatalf("release pod hostUsers = %v, want false", hu)
	}
	env.Spec.SecurityContext = nil
	if hu := r.buildJob(env, img, "j", 60).Spec.Template.Spec.HostUsers; hu != nil {
		t.Fatalf("default release pod hostUsers = %v, want unset", *hu)
	}
}
