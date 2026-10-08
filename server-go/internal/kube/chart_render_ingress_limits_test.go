package kube

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// renderEnvWithIngress renders the whole kusoenvironment chart for a
// public service in namespace kuso.
func renderEnvWithIngress(t *testing.T, sets ...string) string {
	t.Helper()
	helmBin, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not found on PATH; skipping chart render test")
	}
	args := []string{
		"template", "alpha-web-production", chartDir(t), "-n", "kuso",
		"--set", "project=alpha", "--set", "service=web",
		"--set", "image.repository=registry.local/alpha/web", "--set", "image.tag=sha123",
		"--set", "host=web.example.com",
	}
	for _, s := range sets {
		if s == noTraefikCRDs {
			continue
		}
		args = append(args, "--set", s)
	}
	if !slices.Contains(sets, noTraefikCRDs) {
		args = append(args, "--api-versions", "traefik.io/v1alpha1/Middleware")
	}
	out, err := exec.Command(helmBin, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return string(out)
}

// noTraefikCRDs, passed among the sets, renders against a cluster that
// does not serve traefik.io Middlewares.
const noTraefikCRDs = "<no traefik CRDs>"

const (
	inflightRef  = "kuso-alpha-web-production-inflight@kubernetescrd"
	ratelimitRef = "kuso-alpha-web-production-ratelimit@kubernetescrd"
)

// Without the operator's switch the chart must not touch the Ingress:
// an operator that lacks the traefik.io RBAC would fail the whole
// release on a Middleware, and an Ingress naming a Middleware that
// doesn't exist loses its router.
func TestKusoEnvironmentChart_IngressLimitsOffWithoutOperatorSwitch(t *testing.T) {
	t.Parallel()
	for _, sets := range [][]string{nil, {"requestLimits.maxConcurrent=50", "requestLimits.ratePerSecond=10"}} {
		out := renderEnvWithIngress(t, sets...)
		if strings.Contains(out, "kind: Middleware") || strings.Contains(out, "router.middlewares") {
			t.Fatalf("limits rendered without ingressLimitsEnabled (%v):\n%s", sets, out)
		}
	}
}

// Default with the switch on: every public service gets a cap on
// requests in flight, so a flood against one host is answered with 429
// instead of piling up in the shared proxy.
func TestKusoEnvironmentChart_IngressLimitsDefaultCap(t *testing.T) {
	t.Parallel()
	out := renderEnvWithIngress(t, "ingressLimitsEnabled=true")
	for _, want := range []string{
		"kind: Middleware",
		"name: alpha-web-production-inflight",
		"inFlightReq:",
		"amount: 1000",
		"requestHost: true",
		"traefik.ingress.kubernetes.io/router.middlewares: \"" + inflightRef + "\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rateLimit:") {
		t.Errorf("no rate limit is set by default:\n%s", out)
	}
}

func TestKusoEnvironmentChart_IngressLimitsOverrides(t *testing.T) {
	t.Parallel()
	out := renderEnvWithIngress(t, "ingressLimitsEnabled=true",
		"requestLimits.maxConcurrent=200", "requestLimits.ratePerSecond=50", "requestLimits.burst=120")
	for _, want := range []string{
		"amount: 200", "rateLimit:", "average: 50", "burst: 120",
		"router.middlewares: \"" + inflightRef + "," + ratelimitRef + "\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// -1 opts a service out of the cap; the rate limit stands alone.
	out = renderEnvWithIngress(t, "ingressLimitsEnabled=true",
		"requestLimits.maxConcurrent=-1", "requestLimits.ratePerSecond=50")
	if strings.Contains(out, "inFlightReq") || !strings.Contains(out, "router.middlewares: \""+ratelimitRef+"\"") {
		t.Errorf("maxConcurrent=-1 must drop only the in-flight cap:\n%s", out)
	}
	// burst defaults to the rate.
	if !strings.Contains(out, "burst: 50") {
		t.Errorf("burst should default to ratePerSecond:\n%s", out)
	}

	out = renderEnvWithIngress(t, "ingressLimitsEnabled=true", "requestLimits.maxConcurrent=-1")
	if strings.Contains(out, "kind: Middleware") || strings.Contains(out, "router.middlewares") {
		t.Errorf("fully opted out service still has limits:\n%s", out)
	}
}

// No Ingress, no Middleware: internal services, workers and non-Traefik
// ingress classes.
func TestKusoEnvironmentChart_IngressLimitsFollowTheIngress(t *testing.T) {
	t.Parallel()
	for _, set := range []string{"internal=true", "runtime=worker", "ingressClassName=nginx", noTraefikCRDs} {
		out := renderEnvWithIngress(t, "ingressLimitsEnabled=true", set)
		if strings.Contains(out, "kind: Middleware") || strings.Contains(out, "router.middlewares") {
			t.Errorf("%s: limits rendered:\n%s", set, out)
		}
	}
}
