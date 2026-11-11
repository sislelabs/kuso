package kube

import (
	"maps"
	"testing"
)

// The key must equal the charts' `sha256sum | trunc 20` of the conn name,
// or the addon's subscribers policy never admits the pod.
func TestEnvIsolationLabels(t *testing.T) {
	got := EnvIsolationLabels("preview-pr-1", []string{"e2e-db-pr-1-conn", "e2e-files-readonly", "e2e-web-pr-1-secrets"})
	want := map[string]string{
		LabelEnv: "preview-pr-1",
		"conn.kuso.sislelabs.com/6a1a8e2c8ce5027f4d4d": "true", // e2e-db-pr-1-conn
		SubscriberLabelKey("e2e-files-conn"):           "true", // read-only s3 subscribes to its addon
	}
	if !maps.Equal(got, want) {
		t.Errorf("EnvIsolationLabels = %v, want %v", got, want)
	}
	if got := EnvIsolationLabels("", []string{"e2e-db-conn"}); got != nil {
		t.Errorf("unstamped env got labels %v; it must stay legacy", got)
	}
}
