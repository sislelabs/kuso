package github

import (
	"context"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// stubPreviewDB returns a fixed source-conn -> clone-conn map, the shape
// previewdb.Cloner.EnsurePRAddons hands the dispatcher.
type stubPreviewDB struct{ pairs map[string]string }

func (s stubPreviewDB) EnsurePRAddons(context.Context, string, int) ([]string, map[string]string, error) {
	var conns []string
	for _, c := range s.pairs {
		conns = append(conns, c)
	}
	return conns, s.pairs, nil
}

func (stubPreviewDB) DeletePRAddons(context.Context, string, int) error { return nil }

const prOpened7 = `{
	"action": "opened",
	"number": 7,
	"pull_request": {
		"head": {"ref": "feat/x", "sha": "abcdef0123456789abcdef0123456789abcdef01"},
		"base": {"ref": "main"},
		"state": "open"
	},
	"repository": {"full_name": "example/alpha"}
}`

// e2eShapedService is the api service from the e2e run: subscribed to both
// addons and to one shared key (per-key opt-in, not mount-all).
func e2eShapedService(sharedEnvKeys []string) seed {
	return typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api"},
		},
		Spec: kube.KusoServiceSpec{
			Project:          "alpha",
			Port:             3000,
			SubscribedAddons: []string{"cache", "db"},
			SharedEnvKeys:    sharedEnvKeys,
		},
	})
}

// e2eShapedProdEnv is what the production path renders for that service:
// both source conns, the per-service secret, and SHARED_TOKEN as a per-key
// secretKeyRef instead of a blanket shared mount.
func e2eShapedProdEnv() seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api-production", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "production"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "production",
			EnvFromSecrets: []string{"alpha-cache-conn", "alpha-db-conn", "alpha-api-secrets"},
			EnvVars: []kube.KusoEnvVar{{
				Name:      "SHARED_TOKEN",
				ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "SHARED_TOKEN"}},
			}},
			SharedEnvKeys:    []string{"SHARED_TOKEN"},
			SubscribedAddons: []string{"cache", "db"},
		},
	})
}

func e2eShapedDispatcher(t *testing.T, sharedEnvKeys []string, extra ...seed) *Dispatcher {
	t.Helper()
	seeds := append([]seed{
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		e2eShapedService(sharedEnvKeys),
		e2eShapedProdEnv(),
	}, extra...)
	d := newDispatcher(t, seeds...)
	d.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-cache-conn", "alpha-db-conn"}, nil
	}
	d.PreviewDB = stubPreviewDB{pairs: map[string]string{
		"alpha-db-conn":    "alpha-db-pr-7-conn",
		"alpha-cache-conn": "alpha-cache-pr-7-conn",
	}}
	return d
}

func dispatchPR7(t *testing.T, d *Dispatcher) *kube.KusoEnvironment {
	t.Helper()
	if err := d.Dispatch(context.Background(), "pull_request", []byte(prOpened7)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	env, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-7")
	if err != nil {
		t.Fatalf("preview env: %v", err)
	}
	return env
}

func assertNoSourceConns(t *testing.T, envFrom []string) {
	t.Helper()
	for _, src := range []string{"alpha-db-conn", "alpha-cache-conn"} {
		if slices.Contains(envFrom, src) {
			t.Errorf("preview mounts source conn %s next to its clone: %v — any key only in the source (POOLER_*, DIRECT_URL) leaks production credentials", src, envFrom)
		}
	}
	for _, clone := range []string{"alpha-db-pr-7-conn", "alpha-cache-pr-7-conn"} {
		if !slices.Contains(envFrom, clone) {
			t.Errorf("preview missing clone conn %s: %v", clone, envFrom)
		}
	}
}

// F8a: the base env's envFromSecrets carries the SOURCE conns. The clone
// swap only ran over the project addon list, so the base copy survived and
// the preview mounted prod + clone, winning only by envFrom order.
func TestDispatch_PROpened_ReplacesSourceConnWithClone(t *testing.T) {
	t.Parallel()
	env := dispatchPR7(t, e2eShapedDispatcher(t, []string{"SHARED_TOKEN"}))
	assertNoSourceConns(t, env.Spec.EnvFromSecrets)
}

// F8a, resync: an env created before the fix keeps its envFromSecrets across
// PR pushes (to preserve reviewer-set per-env secrets), so the stale source
// conn must be scrubbed on update too, or existing previews never heal.
func TestDispatch_PRSynced_ScrubsSourceConnFromExistingPreview(t *testing.T) {
	t.Parallel()
	stale := typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api-pr-7", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api", kube.LabelEnv: "preview-pr-7"},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "preview",
			PullRequest: &kube.KusoPullRequest{Number: 7},
			EnvFromSecrets: []string{
				"alpha-cache-conn", "alpha-db-conn", "alpha-api-pr-7-secrets",
				"alpha-db-pr-7-conn", "alpha-shared", "kuso-instance-shared",
			},
		},
	})
	env := dispatchPR7(t, e2eShapedDispatcher(t, []string{"SHARED_TOKEN"}, stale))
	assertNoSourceConns(t, env.Spec.EnvFromSecrets)
	if !slices.Contains(env.Spec.EnvFromSecrets, "alpha-api-pr-7-secrets") {
		t.Errorf("resync dropped the preview's own per-env secret: %v", env.Spec.EnvFromSecrets)
	}
	for _, shared := range kube.SharedSecretNames("alpha") {
		if slices.Contains(env.Spec.EnvFromSecrets, shared) {
			t.Errorf("resync kept blanket shared mount %s despite sharedEnvKeys: %v", shared, env.Spec.EnvFromSecrets)
		}
	}
}

// F8b: with a per-key sharedEnvKeys subscription the preview must not
// blanket-mount the shared secrets — production doesn't. The subscribed key
// still arrives through the inherited per-key secretKeyRef.
func TestDispatch_PROpened_HonoursSharedEnvKeys(t *testing.T) {
	t.Parallel()
	env := dispatchPR7(t, e2eShapedDispatcher(t, []string{"SHARED_TOKEN"}))
	for _, shared := range kube.SharedSecretNames("alpha") {
		if slices.Contains(env.Spec.EnvFromSecrets, shared) {
			t.Errorf("preview blanket-mounts %s although the service subscribes per key: %v", shared, env.Spec.EnvFromSecrets)
		}
	}
	found := false
	for _, v := range env.Spec.EnvVars {
		if v.Name == "SHARED_TOKEN" && v.ValueFrom != nil {
			found = true
		}
	}
	if !found {
		t.Errorf("subscribed SHARED_TOKEN secretKeyRef not inherited: %+v", env.Spec.EnvVars)
	}
}

// F8b legacy: nil sharedEnvKeys means mount-all, so the blanket mount stays
// (but only when the base env also has no explicit list).
func TestDispatch_PROpened_NilSharedEnvKeysKeepsBlanketMount(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "api"),
	)
	env := dispatchPR7(t, d)
	for _, shared := range kube.SharedSecretNames("alpha") {
		if !slices.Contains(env.Spec.EnvFromSecrets, shared) {
			t.Errorf("legacy mount-all preview lost %s: %v", shared, env.Spec.EnvFromSecrets)
		}
	}
}

// F9: env-group clone services (label env=<group>) are a separate copy of
// the app for a named env. A PR must preview only the production service;
// previewing the clone too spawned api-qa-pr-N mounting the qa DB + prod cache.
func TestDispatch_PROpened_SkipsEnvGroupCloneServices(t *testing.T) {
	t.Parallel()
	clone := typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-api-qa", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api-qa", kube.LabelEnv: "qa"},
			Annotations: map[string]string{
				"kuso.sislelabs.com/env-group-source-service": "alpha-api",
			},
		},
		Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000},
	})
	prodLabelled := typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "web", kube.LabelEnv: "production"},
		},
		Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000},
	})
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "api"),
		clone,
		prodLabelled,
	)
	dispatchPR7(t, d)
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-qa-pr-7"); !apierrors.IsNotFound(err) {
		t.Errorf("env-group clone service got a PR preview (err=%v)", err)
	}
	if _, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-7"); err != nil {
		t.Errorf("service explicitly labelled env=production lost its preview: %v", err)
	}
}
