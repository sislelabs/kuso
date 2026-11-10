package github

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedEnvCR(e *kube.KusoEnvironment) seed {
	e.Namespace = "kuso"
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func seedSecret(t *testing.T, d *Dispatcher, name string, data map[string]string) {
	t.Helper()
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}, Data: map[string][]byte{}}
	for k, v := range data {
		sec.Data[k] = []byte(v)
	}
	if _, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Create(context.Background(), sec, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed secret %s: %v", name, err)
	}
}

func prodEnvWithSecret() seed {
	return seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "alpha-api-production",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "production"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "production",
			EnvFromSecrets: []string{"alpha-api-production-secrets", "alpha-api-secrets"},
			EnvVars:        []kube.KusoEnvVar{{Name: "MODE", Value: "live"}},
		},
	})
}

// The gate: no staging env, no preview. The refusal is visible (a
// notification), and a preview that already exists is left untouched.
func TestDispatch_PROpened_NoStagingEnvRefusesPreview(t *testing.T) {
	t.Parallel()
	existing := seedPreviewEnv("alpha", "web", 7, "feat/old")
	d := newDispatcherNoStaging(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "api"),
		seedSvc("alpha", "web"),
		prodEnvWithSecret(),
		existing,
	)
	rec := &recordingEmitter{}
	d.Notifier = rec
	if err := d.Dispatch(context.Background(), "pull_request", []byte(prOpened7)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-7"); !apierrors.IsNotFound(err) {
		t.Fatalf("preview created without a staging env (err=%v)", err)
	}
	if bs, _ := d.Builds.List(context.Background(), "alpha", "api"); len(bs) != 0 {
		t.Errorf("preview build triggered without a staging env: %d builds", len(bs))
	}
	kept, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-7")
	if err != nil || kept.Spec.Branch != "feat/old" {
		t.Errorf("existing preview was touched: %+v, err=%v", kept, err)
	}
	if len(rec.events) != 1 || !strings.Contains(rec.events[0].Body, "project has no staging environment") {
		t.Fatalf("refusal not reported: %+v", rec.events)
	}

	// A second push is reported on the PR (commit status) but not
	// re-notified.
	if err := d.Dispatch(context.Background(), "pull_request", []byte(strings.Replace(prOpened7, `"opened"`, `"synchronize"`, 1))); err != nil {
		t.Fatalf("Dispatch sync: %v", err)
	}
	if len(rec.events) != 1 {
		t.Errorf("refusal re-notified on every push: %d events", len(rec.events))
	}
}

// ISO-1: the preview's per-PR Secret is a copy of the STAGING env's Secret;
// production's per-env Secret is never copied or mounted, even when a
// trigger names production as the base.
func TestDispatch_PROpened_CopiesStagingSecretNotProduction(t *testing.T) {
	t.Parallel()
	staging := seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "alpha-api-staging",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "staging"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "custom", Host: "api-staging.alpha.example.com",
			EnvFromSecrets: []string{"alpha-api-secrets", "alpha-api-staging-secrets"},
			EnvVars:        []kube.KusoEnvVar{{Name: "MODE", Value: "test"}},
		},
	})
	proj := seedProj("alpha", "https://github.com/example/alpha", "main", true, 5)
	proj.obj.Object["spec"].(map[string]any)["previews"].(map[string]any)["triggers"] = []any{
		map[string]any{"branch": "main", "baseEnv": "production"},
	}
	d := newDispatcherNoStaging(t, proj, seedSvc("alpha", "api"), prodEnvWithSecret(), staging)
	seedSecret(t, d, "alpha-api-production-secrets", map[string]string{"STRIPE_KEY": "sk_live", "PROD_ONLY": "x"})
	seedSecret(t, d, "alpha-api-staging-secrets", map[string]string{"STRIPE_KEY": "sk_test", "SITE": "https://api-staging.alpha.example.com"})

	env := dispatchPR7(t, d)
	pr, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-api-pr-7-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pr secret: %v", err)
	}
	if got := string(pr.Data["STRIPE_KEY"]); got != "sk_test" {
		t.Errorf("STRIPE_KEY = %q, want staging's sk_test", got)
	}
	if _, leaked := pr.Data["PROD_ONLY"]; leaked {
		t.Errorf("production per-env key copied into the preview: %v", pr.Data)
	}
	if got := string(pr.Data["SITE"]); got != "https://api-pr-7.alpha.example.com" {
		t.Errorf("staging host not rewritten to the preview: %q", got)
	}
	for _, n := range []string{"alpha-api-production-secrets", "alpha-api-secrets", "alpha-api-staging-secrets"} {
		if slices.Contains(env.Spec.EnvFromSecrets, n) {
			t.Errorf("preview mounts %s: %v", n, env.Spec.EnvFromSecrets)
		}
	}
	if !slices.Contains(env.Spec.EnvFromSecrets, "alpha-api-pr-7-secrets") {
		t.Errorf("preview missing its per-PR secret: %v", env.Spec.EnvFromSecrets)
	}
	if !slices.ContainsFunc(env.Spec.EnvVars, func(v kube.KusoEnvVar) bool { return v.Name == "MODE" && v.Value == "test" }) {
		t.Errorf("env vars not inherited from staging: %+v", env.Spec.EnvVars)
	}
}

// An env-group "staging" is a valid base too: the preview copies the clone
// service's secrets, maps the staging DB clone onto its own per-PR clone and
// uses staging's s3 clone instead of production's bucket (ISO-4).
func TestDispatch_PROpened_EnvGroupStagingBase(t *testing.T) {
	t.Parallel()
	group := seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "alpha-api-staging-production",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api-staging", kube.LabelEnv: "staging"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api-staging", Kind: "production",
			EnvFromSecrets: []string{"alpha-db-staging-conn", "alpha-files-staging-conn", "alpha-api-staging-secrets", "alpha-api-staging-staging-secrets"},
			EnvVars: []kube.KusoEnvVar{{
				Name:      "DATABASE_READ_URL",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-db-staging-conn", "key": "DATABASE_URL"}},
			}},
		},
	})
	cloneAddon := func(short, kind, src string) seed {
		return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "alpha-" + short, Namespace: "kuso",
				Labels:      map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "staging"},
				Annotations: map[string]string{"kuso.sislelabs.com/env-group-source-addon": "alpha-" + src},
			},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: kind},
		})
	}
	d := newDispatcherNoStaging(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "api"),
		prodEnvWithSecret(),
		group,
		seedPreviewAddon("db", "postgres", false),
		seedPreviewAddon("files", "s3", false),
		cloneAddon("db-staging", "postgres", "db"),
		cloneAddon("files-staging", "s3", "files"),
	)
	d.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-files-conn"}, nil
	}
	d.PreviewDB = stubPreviewDB{pairs: map[string]string{"alpha-db-conn": "alpha-db-pr-7-conn"}}
	seedSecret(t, d, "alpha-api-production-secrets", map[string]string{"STRIPE_KEY": "sk_live"})
	seedSecret(t, d, "alpha-api-staging-secrets", map[string]string{"STRIPE_KEY": "sk_test", "A": "svc"})
	seedSecret(t, d, "alpha-api-staging-staging-secrets", map[string]string{"A": "env"})

	env := dispatchPR7(t, d)
	got := env.Spec.EnvFromSecrets
	for _, bad := range []string{"alpha-db-conn", "alpha-db-staging-conn", "alpha-files-conn", "alpha-api-staging-secrets", "alpha-api-production-secrets"} {
		if slices.Contains(got, bad) {
			t.Errorf("preview mounts %s: %v", bad, got)
		}
	}
	for _, want := range []string{"alpha-db-pr-7-conn", "alpha-files-staging-conn", "alpha-api-pr-7-secrets"} {
		if !slices.Contains(got, want) {
			t.Errorf("preview missing %s: %v", want, got)
		}
	}
	for _, v := range env.Spec.EnvVars {
		ref, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
		if v.Name == "DATABASE_READ_URL" && ref["name"] != "alpha-db-pr-7-conn" {
			t.Errorf("staging DB ref not moved to the per-PR clone: %+v", ref)
		}
	}
	pr, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-api-pr-7-secrets", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pr secret: %v", err)
	}
	if string(pr.Data["STRIPE_KEY"]) != "sk_test" || string(pr.Data["A"]) != "env" {
		t.Errorf("pr secret = %v, want staging clone's secrets with the env secret winning", pr.Data)
	}
}

// With both a plain staging env and a staging env group, the one tracking
// the PR's base branch wins; otherwise the plain env.
func TestResolvePreviewBase_PrefersBranchMatch(t *testing.T) {
	t.Parallel()
	plain := seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-staging", Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "staging"}},
		Spec:       kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-api", Branch: "develop"},
	})
	group := seedEnvCR(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-staging-production", Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelEnv: "staging"}},
		Spec:       kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-api-staging", Branch: "staging"},
	})
	d := newDispatcherNoStaging(t, plain, group)
	ctx := context.Background()
	for ref, want := range map[string]string{"staging": "alpha-api-staging-production", "develop": "alpha-api-staging", "main": "alpha-api-staging"} {
		b, err := d.resolvePreviewBase(ctx, "kuso", "alpha", "alpha-api", ref, "")
		if err != nil {
			t.Fatalf("base ref %s: %v", ref, err)
		}
		if b.Env.Name != want {
			t.Errorf("base ref %s: got %s, want %s", ref, b.Env.Name, want)
		}
	}
	if _, err := d.resolvePreviewBase(ctx, "kuso", "alpha", "alpha-web", "main", ""); err == nil {
		t.Error("service without a staging env resolved a base")
	}
}

// Resync of an existing preview whose staging has no per-env Secret keeps
// the per-PR Secret it already has instead of leaving it with none.
func TestDispatch_PRSynced_KeepsPRSecretWithoutStagingSecret(t *testing.T) {
	t.Parallel()
	prev := seedPreviewEnv("alpha", "api", 7, "feat/x")
	prev.obj.Object["spec"].(map[string]any)["envFromSecrets"] = []any{"alpha-api-pr-7-secrets"}
	d := newDispatcher(t, seedProj("alpha", "https://github.com/example/alpha", "main", true, 5), seedSvc("alpha", "api"), prev)
	seedSecret(t, d, "alpha-api-pr-7-secrets", map[string]string{"K": "old"})
	if err := d.Dispatch(context.Background(), "pull_request", []byte(strings.Replace(prOpened7, `"opened"`, `"synchronize"`, 1))); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env.Spec.EnvFromSecrets, "alpha-api-pr-7-secrets") {
		t.Errorf("resync dropped the existing per-PR secret: %v", env.Spec.EnvFromSecrets)
	}
	sec, err := d.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-api-pr-7-secrets", metav1.GetOptions{})
	if err != nil || string(sec.Data["K"]) != "old" {
		t.Errorf("per-PR secret rewritten without a staging secret: %v err=%v", sec, err)
	}
}
