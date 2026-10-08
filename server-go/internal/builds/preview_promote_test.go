package builds

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedPreviewPREnv(project, service string, pr int, branch string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-pr-7",
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelService: service,
				kube.LabelEnv:     "preview-pr-7",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project, Service: project + "-" + service, Kind: "preview", Branch: branch,
			PullRequest: &kube.KusoPullRequest{Number: pr, HeadRef: branch},
		},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func envTag(t *testing.T, s *Service, name string) string {
	t.Helper()
	e, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get env %s: %v", name, err)
	}
	if e.Spec.Image == nil {
		return ""
	}
	return e.Spec.Image.Tag
}

// BLD-9: a release PR staging -> main has head branch `staging`, which the
// persistent staging env tracks. The preview build used to be promoted by
// branch name onto staging, carrying per-PR hosts and clone credentials.
func TestPromoteImage_PreviewBuildStaysInItsPreviewEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedLabelledEnv("alpha", "web", "alpha-web-staging", "staging", "staging"),
		seedPreviewPREnv("alpha", "web", 7, "staging"),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}
	preview := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-prev", Namespace: "kuso",
			Annotations: map[string]string{AnnPreviewEnv: "alpha-web-pr-7", AnnPreviewPR: "7"},
		},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "staging",
			Image: &kube.KusoImage{Repository: "r", Tag: "previewtag"},
		},
	}
	if err := p.promoteImage(context.Background(), "kuso", preview); err != nil {
		t.Fatalf("promote preview build: %v", err)
	}
	if got := envTag(t, s, "alpha-web-staging"); got == "previewtag" {
		t.Error("preview build was promoted onto the persistent staging env")
	}
	if got := envTag(t, s, "alpha-web-pr-7"); got != "previewtag" {
		t.Errorf("preview env tag = %q, want previewtag", got)
	}

	push := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-push", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Branch: "staging",
			Image: &kube.KusoImage{Repository: "r", Tag: "pushtag"},
		},
	}
	if err := p.promoteImage(context.Background(), "kuso", push); err != nil {
		t.Fatalf("promote push build: %v", err)
	}
	if got := envTag(t, s, "alpha-web-staging"); got != "pushtag" {
		t.Errorf("staging env tag = %q, want pushtag", got)
	}
	if got := envTag(t, s, "alpha-web-pr-7"); got != "previewtag" {
		t.Errorf("push build overwrote the preview env (tag %q)", got)
	}
}
