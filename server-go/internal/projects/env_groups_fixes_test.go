package projects

import (
	"context"
	"errors"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// A transient addon-list failure used to be read as "no addons", so the
// group was created with no policy entries and its clones kept production's
// conns. It must fail before anything is created.
func TestCreateEnvGroup_AddonListErrorFails(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "kusoaddons", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver blip")
	})

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err == nil {
		t.Fatal("CreateEnvGroup succeeded despite the addon list failing")
	}
	if _, err := s.Kube.GetKusoService(context.Background(), "kuso", "acme-web-staging"); !apierrors.IsNotFound(err) {
		t.Fatalf("clone service exists after a failed create: err=%v", err)
	}
}

// The push dispatcher gates builds on the service's repo.defaultBranch, so
// moving a group clone's env to another branch must move the clone service
// too, or every production-branch push keeps rebuilding the clone.
func TestSetServiceBranchInEnv_MovesCloneServiceBranch(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080, Repo: &kube.KusoRepoRef{URL: "https://github.com/acme/web", DefaultBranch: "main"}}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if err := s.SetServiceBranchInEnv(ctx, "acme", "staging", "web", "develop"); err != nil {
		t.Fatalf("SetServiceBranchInEnv: %v", err)
	}
	clone, err := s.Kube.GetKusoService(ctx, "kuso", "acme-web-staging")
	if err != nil {
		t.Fatal(err)
	}
	if got := coalesce(clone.Spec.Repo); got.DefaultBranch != "develop" || got.URL != "https://github.com/acme/web" {
		t.Fatalf("clone repo = %+v, want develop on the same URL", got)
	}
	prod, err := s.Kube.GetKusoService(ctx, "kuso", "acme-web")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Spec.Repo.DefaultBranch != "main" {
		t.Fatalf("production service branch moved to %q", prod.Spec.Repo.DefaultBranch)
	}
}

// A service that isn't labelled as the group's clone keeps its own branch.
func TestSetServiceBranchInEnv_LeavesUnlabelledServiceAlone(t *testing.T) {
	t.Parallel()
	env := typedSeed(kube.GVREnvironments, "KusoEnvironment", "acme-web-qa-production", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "acme-web-qa-production", Namespace: "kuso",
			Labels: map[string]string{labelProject: "acme", labelService: "web-qa", labelEnv: "qa"},
		},
		Spec: kube.KusoEnvironmentSpec{Project: "acme", Service: "acme-web-qa", Kind: "production", Branch: "main"},
	})
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{}),
		seedService("acme", "web-qa", kube.KusoServiceSpec{Project: "acme", Repo: &kube.KusoRepoRef{DefaultBranch: "main"}}),
		env,
	)
	ctx := context.Background()
	if err := s.SetServiceBranchInEnv(ctx, "acme", "qa", "web", "develop"); err != nil {
		t.Fatalf("SetServiceBranchInEnv: %v", err)
	}
	svc, _ := s.Kube.GetKusoService(ctx, "kuso", "acme-web-qa")
	if svc.Spec.Repo.DefaultBranch != "main" {
		t.Fatalf("unlabelled service branch changed to %q", svc.Spec.Repo.DefaultBranch)
	}
}

func seedServiceCron(name, service, short, kind string) seed {
	return typedSeed(kube.GVRCrons, "KusoCron", name, &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			Labels: map[string]string{labelProject: "acme", labelService: service, "kuso.sislelabs.com/cron": short},
		},
		Spec: kube.KusoCronSpec{
			Project: "acme", Kind: kind, Service: service, Schedule: "*/5 * * * *",
			Command:        []string{"node", "sync.js", short},
			Image:          &kube.KusoImage{Repository: "reg/acme/web", Tag: "old"},
			EnvFromSecrets: []string{"acme-db-conn", "acme-web-secrets"},
		},
	})
}

// Every kind=service cron on a cloned service is cloned onto the group's
// clone, running the clone's image with the clone env's secrets — not just
// whichever cron happened to be recreated by hand (puncho staging had the
// operator's cron but none of web's).
func TestCreateEnvGroup_ClonesEveryServiceCron(t *testing.T) {
	t.Parallel()
	prodEnv := typedSeed(kube.GVREnvironments, "KusoEnvironment", "acme-web-production", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "acme-web-production", Namespace: "kuso",
			Labels: map[string]string{labelProject: "acme", labelService: "web", labelEnv: "production"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "acme", Service: "acme-web", Kind: "production", Branch: "main",
			Image: &kube.KusoImage{Repository: "reg/acme/web", Tag: "v2"},
		},
	})
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		prodEnv,
		seedServiceCron("acme-web-sync-recent", "acme-web", "sync-recent", ""),
		seedServiceCron("acme-web-sync-week", "acme-web", "sync-week", "service"),
		seedServiceCron("acme-ping", "", "ping", "http"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	cloneEnv, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-web-staging-production")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme-web-staging-sync-recent", "acme-web-staging-sync-week"} {
		c, err := s.Kube.GetKusoCron(ctx, "kuso", name)
		if err != nil {
			t.Fatalf("cron %s not cloned: %v", name, err)
		}
		if c.Spec.Service != "acme-web-staging" {
			t.Errorf("%s: spec.service = %q, want the clone", name, c.Spec.Service)
		}
		if c.Spec.Image == nil || c.Spec.Image.Tag != "v2" {
			t.Errorf("%s: image = %+v, want the clone's v2", name, c.Spec.Image)
		}
		if !slices.Equal(c.Spec.EnvFromSecrets, cloneEnv.Spec.EnvFromSecrets) {
			t.Errorf("%s: envFromSecrets = %v, want the clone env's %v", name, c.Spec.EnvFromSecrets, cloneEnv.Spec.EnvFromSecrets)
		}
		if c.Labels[labelEnv] != "staging" || len(c.OwnerReferences) != 1 || c.OwnerReferences[0].Name != "acme-web-staging" {
			t.Errorf("%s: labels=%v owners=%v, want env=staging owned by the clone", name, c.Labels, c.OwnerReferences)
		}
	}
	all, _ := s.Kube.ListKusoCrons(ctx, "kuso")
	if len(all) != 5 {
		t.Fatalf("got %d crons, want 5 (3 seeded + 2 clones; the http cron is project-scoped)", len(all))
	}
}
