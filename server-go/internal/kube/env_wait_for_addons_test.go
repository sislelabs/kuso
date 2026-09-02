package kube

import (
	"strings"
	"testing"
)

// App pods start before their addon accepts connections often enough that
// every deploy produced a "pod crashed" alert: the app pings the DB on boot,
// gets connection refused (addon reconciling, pooler still coming up, CNI
// policy not yet programmed for the new pod), exits, and kube restarts it.
// The release Job has always carried a wait-for-addons initContainer for
// exactly this; the app Deployment didn't.
func TestKusoEnvironmentChart_WaitForAddonsInit(t *testing.T) {
	out := helmTemplate(t, "alpha-web-production", "envFromSecrets[0]=alpha-db-conn")

	if !strings.Contains(out, "initContainers:") || !strings.Contains(out, "name: wait-for-addons") {
		t.Fatalf("deployment has no wait-for-addons initContainer.\nRendered:\n%s", out)
	}
	if !strings.Contains(out, "image: busybox:1.36") {
		t.Errorf("init image should match the release Job's (busybox:1.36)")
	}
	// It must see the same conn secrets as the app, or it waits on nothing.
	initIdx := strings.Index(out, "name: wait-for-addons")
	appIdx := strings.Index(out, "- name: app")
	if initIdx < 0 || appIdx < 0 || initIdx > appIdx {
		t.Fatalf("initContainer must render before the app container")
	}
	if !strings.Contains(out[initIdx:appIdx], `name: "alpha-db-conn"`) {
		t.Errorf("initContainer does not mount the env's conn secrets; it would wait on nothing.\n%s", out[initIdx:appIdx])
	}
	// The script body made it through `quote` intact.
	if !strings.Contains(out, "wait-for-addons: waiting for") {
		t.Errorf("script body missing from the rendered command")
	}
}
