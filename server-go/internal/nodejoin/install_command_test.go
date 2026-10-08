package nodejoin

import (
	"strings"
	"testing"
)

// A join must install the control plane's k3s version, not whatever the
// stable channel is that day: a kubelet newer than the apiserver is
// outside the Kubernetes skew policy.
func TestBuildInstallCommand_PinsK3sVersion(t *testing.T) {
	cmd := BuildInstallCommand("https://kp:6443", "tok", nil, "", "v1.35.4+k3s1")
	if !strings.Contains(cmd, `INSTALL_K3S_VERSION='v1.35.4+k3s1'`) {
		t.Errorf("join not pinned to the server's k3s version: %s", cmd)
	}
	if strings.Contains(BuildInstallCommand("https://kp:6443", "tok", nil, "", ""), "INSTALL_K3S_VERSION") {
		t.Error("unknown server version should leave the install unpinned")
	}
}

func TestPinnableK3sVersion(t *testing.T) {
	for in, want := range map[string]string{
		"v1.35.4+k3s1":    "v1.35.4+k3s1",
		"v1.30.10+k3s2":   "v1.30.10+k3s2",
		"v1.35.4":         "", // not a k3s apiserver: no k3s tag to pin
		"v1.29.0-eks-123": "",
		"":                "",
		"v1.35.4+k3s1; rm": "",
	} {
		if got := PinnableK3sVersion(in); got != want {
			t.Errorf("PinnableK3sVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
