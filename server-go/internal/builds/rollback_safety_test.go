package builds

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedBranchBuild(project, service, name, branch, tag string) seed {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "kuso",
			Annotations: map[string]string{annPhase: "succeeded"},
		},
		Spec: kube.KusoBuildSpec{
			Project: project,
			Service: project + "-" + service,
			Branch:  branch,
			Image:   &kube.KusoImage{Repository: "reg/" + project + "/" + service, Tag: tag},
		},
	}
	return seedBuild(b)
}

func seedLabelledEnv(project, service, crName, group, branch string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      crName,
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelService: service,
				kube.LabelEnv:     group,
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Branch: branch},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func TestRollback_RefusesBranchMismatchUnlessForced(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"), // branch unset = default branch (main)
		seedBranchBuild("alpha", "web", "alpha-web-feat", "feature-x", "feat123456789"),
	)
	ctx := context.Background()
	_, err := s.Rollback(ctx, "alpha", "web", "production", "alpha-web-feat", RollbackOptions{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("feature-branch build onto production: want ErrInvalid, got %v", err)
	}
	env, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	if env.Spec.Image != nil && env.Spec.Image.Tag == "feat123456789" {
		t.Fatal("refused rollback still patched the env")
	}
	env, err = s.Rollback(ctx, "alpha", "web", "production", "alpha-web-feat", RollbackOptions{Force: true})
	if err != nil {
		t.Fatalf("forced rollback: %v", err)
	}
	if env.Spec.Image == nil || env.Spec.Image.Tag != "feat123456789" {
		t.Errorf("forced rollback did not patch the image: %+v", env.Spec.Image)
	}
}

func TestRollback_SameBranchAllowed(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedLabelledEnv("alpha", "web", "alpha-web-staging", "staging", "develop"),
		seedBranchBuild("alpha", "web", "alpha-web-dev1", "develop", "dev1234567890"),
	)
	env, err := s.Rollback(context.Background(), "alpha", "web", "staging", "alpha-web-dev1", RollbackOptions{})
	if err != nil {
		t.Fatalf("same-branch rollback: %v", err)
	}
	if env.Name != "alpha-web-staging" {
		t.Errorf("rolled back %q, want alpha-web-staging", env.Name)
	}
}

func TestRollback_ResolvesPreviewEnvByLabel(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		// The dispatcher names previews <fqn>-pr-N and labels them
		// preview-pr-N; the UI sends the label.
		seedLabelledEnv("alpha", "web", "alpha-web-pr-7", "preview-pr-7", "feature-x"),
		seedBranchBuild("alpha", "web", "alpha-web-feat", "feature-x", "feat123456789"),
	)
	env, err := s.Rollback(context.Background(), "alpha", "web", "preview-pr-7", "alpha-web-feat", RollbackOptions{})
	if err != nil {
		t.Fatalf("preview rollback: %v", err)
	}
	if env.Name != "alpha-web-pr-7" || env.Spec.Image == nil || env.Spec.Image.Tag != "feat123456789" {
		t.Errorf("preview env not patched: name=%s image=%+v", env.Name, env.Spec.Image)
	}
}

func TestRollback_RefusalsAreInvalidNotInternal(t *testing.T) {
	t.Parallel()
	failed := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-failed", Namespace: "kuso", Annotations: map[string]string{annPhase: "failed"}},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Image: &kube.KusoImage{Repository: "r", Tag: "t"}},
	}
	noImg := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-noimg", Namespace: "kuso", Annotations: map[string]string{annPhase: "succeeded"}},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web"},
	}
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedBuild(failed),
		seedBuild(noImg),
	)
	s.RecordLookup = stubRecordLookup{
		"alpha-web-pruned":    {repo: RegistryHost + "/alpha/web", phase: "succeeded"},
		"alpha-web-oldfailed": {repo: RegistryHost + "/alpha/web", tag: "x", phase: "failed"},
		"alpha-api-other":     {repo: RegistryHost + "/alpha/api", tag: "apitag", phase: "succeeded"},
	}
	ctx := context.Background()
	for _, name := range []string{"alpha-web-failed", "alpha-web-noimg", "alpha-web-pruned", "alpha-web-oldfailed"} {
		if _, err := s.Rollback(ctx, "alpha", "web", "production", name, RollbackOptions{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	// An archived build of a sibling service must not be rolled onto web.
	if _, err := s.Rollback(ctx, "alpha", "web", "production", "alpha-api-other", RollbackOptions{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived sibling build: want ErrNotFound, got %v", err)
	}
}

func TestRollback_EmitsRolledBackEvent(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
	)
	em := &recordingEmitter{}
	s.Notifier = em
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-oldsha", RollbackOptions{Actor: "ivo"}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !em.has(eventDeployRolledBack) {
		t.Fatalf("want a %s event, got %v", eventDeployRolledBack, em.types())
	}
	ev := em.events[0]
	if ev.Project != "alpha" || ev.Service != "web" || ev.Env != "production" {
		t.Errorf("event scope = %s/%s/%s", ev.Project, ev.Service, ev.Env)
	}
}

type stubRecord struct{ repo, tag, phase string }

type stubRecordLookup map[string]stubRecord

func (m stubRecordLookup) GetBuildImage(_ context.Context, _ string, name string) (string, string, string, bool, error) {
	r, ok := m[name]
	return r.repo, r.tag, r.phase, ok, nil
}
