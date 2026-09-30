package builds

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

func TestRedactSecrets(t *testing.T) {
	t.Parallel()
	vals := []string{"hunter2secret", "hunter2secret-and-more", "abc"}
	got := redactSecrets("a=hunter2secret-and-more b=hunter2secret c=abc", vals)
	want := "a=[redacted] b=[redacted] c=abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A failing migration that prints its DATABASE_URL must not carry the
// password into the build annotations or the notification.
func TestMarkReleaseFailed_RedactsSecretValues(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://app:S3cretPassw0rd@alpha-db:5432/app"
	env := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-api-production",
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: "alpha",
				kube.LabelService: "api",
				kube.LabelEnv:     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "production",
			EnvFromSecrets: []string{"alpha-db-conn"},
		},
	}
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-def", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha", Service: "alpha-api", Ref: "def",
			Image: &kube.KusoImage{Repository: "registry/alpha/api", Tag: "def"},
		},
	}
	s := fakeService(t,
		seedBuild(build),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env),
	)
	mkSecret(t, s, "alpha-db-conn", "DATABASE_URL", dsn)
	em := &recordingEmitter{}
	p := &Poller{Svc: s, Notifier: em}
	tail := "connecting to " + dsn + "\nerror: relation \"users\" does not exist"
	p.markReleaseFailed(context.Background(), "kuso", build, env, releaserun.Result{
		Outcome: releaserun.OutcomeFailed, JobName: "rel", LogTail: tail, LogTailLong: "migrating\n" + tail,
	})
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-api-def")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	for _, k := range []string{annMessage, annReleaseLogTail} {
		v := got.Annotations[k]
		if strings.Contains(v, "S3cretPassw0rd") {
			t.Errorf("%s leaks the secret: %q", k, v)
		}
		if !strings.Contains(v, "does not exist") {
			t.Errorf("%s lost the non-secret error text: %q", k, v)
		}
	}
	if len(em.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(em.events))
	}
	if ev := em.events[0]; strings.Contains(ev.Body+ev.Description, "S3cretPassw0rd") {
		t.Errorf("notification leaks the secret: %q", ev.Body)
	}
}
