package builds

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// B-02: a queued / CI-gated build must clone with a token minted at
// dispatch, not the 1h one minted at Create.
func TestPromoteOne_RefreshesCloneToken(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-q", Namespace: "kuso",
			Labels: map[string]string{"kuso.sislelabs.com/build-state": "queued"}},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-web", Ref: cloneTokenTestRef,
			Repo:                 &kube.KusoRepoRef{URL: "https://github.com/example/web"},
			GithubInstallationID: 4242,
		},
	}
	s := fakeService(t, seedService("alpha", "web"), seedBuild(b))
	if _, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-q-token", Namespace: "kuso"},
		StringData: map[string]string{"token": "EXPIRED"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	m := &recordingMinter{}
	s.Tokens = m
	p := &Poller{Svc: s, Logger: slog.Default()}
	if !p.promoteOne(context.Background(), "kuso", "alpha", "alpha-web", b) {
		t.Fatal("promoteOne returned false")
	}
	if m.scoped != 1 {
		t.Errorf("scoped mints at dispatch = %d, want 1", m.scoped)
	}
	if tok := cloneToken(t, s, "alpha-web-q"); tok != "SCOPED-TOKEN" {
		t.Errorf("clone token at dispatch = %q, want a freshly minted one", tok)
	}
}
