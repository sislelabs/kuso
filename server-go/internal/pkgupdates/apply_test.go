package pkgupdates

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildApplyJob(t *testing.T) {
	t.Parallel()
	w := &Watcher{}

	// allowReboot=false → ALLOW_REBOOT env "false", pinned to node,
	// privileged, reuses the probe SA.
	j := w.buildApplyJob("server2", "apt", false)
	if j.Name != "kuso-pkg-apply-server2" || j.Namespace != "kuso" {
		t.Errorf("job meta: %s/%s", j.Namespace, j.Name)
	}
	pod := j.Spec.Template.Spec
	if pod.NodeName != "server2" {
		t.Errorf("not pinned to node: %q", pod.NodeName)
	}
	if !pod.HostPID {
		t.Error("hostPID must be true (nsenter host)")
	}
	if pod.ServiceAccountName != "kuso-pkg-probe" {
		t.Errorf("SA = %q, want kuso-pkg-probe", pod.ServiceAccountName)
	}
	c := pod.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Error("container must be privileged")
	}
	if envVal(c.Env, "ALLOW_REBOOT") != "false" || envVal(c.Env, "PKG_MGR") != "apt" {
		t.Errorf("env: ALLOW_REBOOT=%q PKG_MGR=%q", envVal(c.Env, "ALLOW_REBOOT"), envVal(c.Env, "PKG_MGR"))
	}

	// allowReboot=true → ALLOW_REBOOT "true".
	if envVal(w.buildApplyJob("n", "apt", true).Spec.Template.Spec.Containers[0].Env, "ALLOW_REBOOT") != "true" {
		t.Error("allowReboot=true should set ALLOW_REBOOT=true")
	}
}

func envVal(env []corev1.EnvVar, k string) string {
	for _, e := range env {
		if e.Name == k {
			return e.Value
		}
	}
	return ""
}
