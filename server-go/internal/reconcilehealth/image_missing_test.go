package reconcilehealth

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

type fakeProber struct {
	present map[string]bool // "repo:tag" -> exists
	err     error
}

func (f fakeProber) ResolveTagDigest(_ context.Context, repo, tag string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.present[repo+":"+tag] {
		return "sha256:" + tag, nil
	}
	return "", nil // 404 contract: empty digest, nil error
}

func envWithImage(name, repo, tag string) *kube.KusoEnvironment {
	return &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "tickero", Service: "tickero-api", Branch: "stage",
			Image: &kube.KusoImage{Repository: repo, Tag: tag},
		},
	}
}

// Five staging envs ran for weeks on images the registry no longer had —
// their nodes still cached the layers, so nothing failed until a reschedule
// hit ImagePullBackOff. `kuso health` said 94/96 healthy the whole time: it
// classifies CR conditions, and the CRs were fine. The registry is the only
// thing that knows, so ask it.
func TestClassifyEnvImage_MissingFromRegistry(t *testing.T) {
	const reg = "kuso-registry.kuso.svc.cluster.local:5000"
	env := envWithImage("tickero-api-staging", reg+"/tickero/api", "stage-gone")
	prober := fakeProber{present: map[string]bool{"tickero/api:stage-live": true}}

	iss, ok := ClassifyEnvImage(context.Background(), prober, reg, env)
	if !ok {
		t.Fatal("expected an issue for an env whose image tag is not in the registry")
	}
	if iss.Kind != KindImageMissingFromRegistry {
		t.Errorf("Kind = %q", iss.Kind)
	}
	if iss.Severity != SeverityCritical {
		t.Errorf("Severity = %q, want critical — the env is one reschedule away from ImagePullBackOff", iss.Severity)
	}
	if !strings.Contains(iss.Fix, "kuso redeploy tickero api --branch stage") {
		t.Errorf("Fix should be the exact rebuild command, got %q", iss.Fix)
	}
}

func TestClassifyEnvImage_PresentIsHealthy(t *testing.T) {
	const reg = "kuso-registry.kuso.svc.cluster.local:5000"
	env := envWithImage("tickero-api-production", reg+"/tickero/api", "main-live")
	prober := fakeProber{present: map[string]bool{"tickero/api:main-live": true}}
	if _, ok := ClassifyEnvImage(context.Background(), prober, reg, env); ok {
		t.Error("an image that exists must not be flagged")
	}
}

// Only the in-cluster registry is probed: a runtime=image service pulling
// ghcr.io/... is the user's registry, and a transient probe error is not
// evidence of a missing image.
func TestClassifyEnvImage_SkipsExternalAndTransient(t *testing.T) {
	const reg = "kuso-registry.kuso.svc.cluster.local:5000"
	ext := envWithImage("x", "ghcr.io/acme/app", "v1")
	if _, ok := ClassifyEnvImage(context.Background(), fakeProber{}, reg, ext); ok {
		t.Error("external registry image must not be probed")
	}
	env := envWithImage("y", reg+"/tickero/api", "t")
	if _, ok := ClassifyEnvImage(context.Background(), fakeProber{err: errors.New("dial tcp: timeout")}, reg, env); ok {
		t.Error("a probe error must not be reported as a missing image")
	}
	if _, ok := ClassifyEnvImage(context.Background(), nil, reg, env); ok {
		t.Error("no prober wired (tests, early boot) must be a no-op")
	}
}
