package kube

import "testing"

func TestSharedSecretNames(t *testing.T) {
	got := SharedSecretNames("alpha")
	want := []string{"alpha-shared", "kuso-instance-shared"}
	if len(got) != len(want) {
		t.Fatalf("SharedSecretNames len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SharedSecretNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
