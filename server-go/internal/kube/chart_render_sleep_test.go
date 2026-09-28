package kube

import (
	"strings"
	"testing"
)

// autoSleep (written by the scaledown watcher for non-production envs)
// must route the Ingress through the activator exactly like sleep.enabled,
// or a slept preview/staging env 503s with nothing to wake it.
func TestKusoEnvironmentChart_AutoSleepRoutesViaActivator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		ns          string
		sets        []string
		wantBackend string
		wantMirror  bool
	}{
		{"home_ns_autosleep", "kuso", []string{"kind=preview", "autoSleep=true"}, "name: kuso-activator", false},
		{"custom_ns_autosleep_uses_mirror", "kuso-alpha", []string{"kind=custom", "autoSleep=true"}, "name: test-env-activator", true},
		{"autosleep_off_routes_to_app", "kuso", []string{"kind=custom", "autoSleep=false"}, "name: test-env\n", false},
		{"autosleep_wildcard_ingress", "kuso", []string{"autoSleep=true", "wildcardDomains[0].host=*.example.com", "wildcardDomains[0].tlsSecret=wc"}, "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := helmTemplateEnvNS(t, tc.ns, tc.sets...)
			if tc.wantBackend != "" && !strings.Contains(out, tc.wantBackend) {
				t.Errorf("expected backend %q, got:\n%s", tc.wantBackend, out)
			}
			if got := strings.Contains(out, "externalName: kuso-activator.kuso.svc.cluster.local"); got != tc.wantMirror {
				t.Errorf("ExternalName mirror present=%v, want %v", got, tc.wantMirror)
			}
			if tc.name == "autosleep_wildcard_ingress" {
				wc := out[strings.Index(out, "name: test-env-wildcard"):]
				if !strings.Contains(wc, "name: kuso-activator") {
					t.Errorf("wildcard Ingress not routed via activator:\n%s", wc)
				}
			}
		})
	}
}

// Sleep on an HPA-managed env relies on Kubernetes' implicit HPA
// maintenance mode: scaledown sets the Deployment to 0 and the HPA stops
// acting (minReplicas > 0 and target replicas == 0) until the activator
// raises replicas again. That only holds while the chart (a) never stamps
// spec.replicas on an HPA env — otherwise every helm-operator reconcile
// would fight the 0 — and (b) never renders minReplicas 0.
func TestKusoEnvironmentChart_HPAEnvLeavesReplicasToSleep(t *testing.T) {
	t.Parallel()
	out := helmTemplateEnvNS(t, "kuso",
		"autoscaling.enabled=true", "autoscaling.minReplicas=1", "autoscaling.maxReplicas=5",
		"replicaCount=0", "autoSleep=true")
	dep := renderedDoc(t, out, "kind: Deployment")
	if strings.Contains(dep, "\n  replicas:") {
		t.Errorf("HPA-managed Deployment stamps spec.replicas; the operator would undo sleep:\n%s", dep)
	}
	hpa := renderedDoc(t, out, "kind: HorizontalPodAutoscaler")
	if !strings.Contains(hpa, "minReplicas: 1") {
		t.Errorf("HPA minReplicas must stay >= 1 for implicit maintenance mode:\n%s", hpa)
	}

	// Static-replica env: the sleep pin is replicaCount=0 on the env CR.
	static := renderedDoc(t, helmTemplateEnvNS(t, "kuso", "replicaCount=0", "autoSleep=true"), "kind: Deployment")
	if !strings.Contains(static, "\n  replicas: 0") {
		t.Errorf("static env with replicaCount=0 should render replicas: 0:\n%s", static)
	}
}

// renderedDoc returns the YAML document in a multi-doc render containing marker.
func renderedDoc(t *testing.T, out, marker string) string {
	t.Helper()
	for _, doc := range strings.Split(out, "\n---") {
		if strings.Contains(doc, marker) {
			return doc
		}
	}
	t.Fatalf("no document containing %q in render", marker)
	return ""
}
