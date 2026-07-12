package kube

import "testing"

func TestValidateSecurityContext(t *testing.T) {
	t.Parallel()
	strp := func(caps ...string) *KusoSecurityContext {
		return &KusoSecurityContext{Capabilities: &KusoCapabilities{Add: caps}}
	}
	cases := []struct {
		name string
		sc   *KusoSecurityContext
		ok   bool
	}{
		{"nil ctx", nil, true},
		{"nil caps", &KusoSecurityContext{}, true},
		{"empty add", strp(), true},
		{"allowed single", strp("NET_BIND_SERVICE"), true},
		{"allowed several", strp("SETUID", "SETGID", "CHOWN"), true},
		{"cap_ prefix tolerated", strp("CAP_SETUID"), true},
		{"lowercase tolerated", strp("setuid"), true},
		{"dangerous SYS_ADMIN", strp("SYS_ADMIN"), false},
		{"dangerous NET_ADMIN", strp("NET_ADMIN"), false},
		{"dangerous SYS_PTRACE", strp("SYS_PTRACE"), false},
		{"ALL rejected", strp("ALL"), false},
		{"one bad among good", strp("SETUID", "SYS_ADMIN"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSecurityContext(tc.sc)
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}
