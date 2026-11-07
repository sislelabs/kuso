package releaserun

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func guardRunner(t *testing.T, addons ...*kube.KusoAddon) (*Runner, *k8sfake.Clientset) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRAddons: "KusoAddonList"})
	for _, a := range addons {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(a)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: kube.GVRAddons.Group, Version: kube.GVRAddons.Version, Kind: "KusoAddon"})
		if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	cs := k8sfake.NewSimpleClientset()
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}), cs
}

func guardAddon(name, kind, group string) *kube.KusoAddon {
	labels := map[string]string{kube.LabelProject: "acme"}
	if group != "" {
		labels[kube.LabelEnv] = group
	}
	return &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels},
		Spec:       kube.KusoAddonSpec{Project: "acme", Kind: kind},
	}
}

func guardEnv(group string, envFrom []string, vars ...kube.KusoEnvVar) *kube.KusoEnvironment {
	return &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "acme-web-" + group + "-production", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "acme", kube.LabelEnv: group},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: "acme", Service: "acme-web-" + group, Kind: "production",
			EnvFromSecrets: envFrom, EnvVars: vars,
			Release: &kube.KusoReleaseSpec{Command: []string{"migrate"}},
		},
	}
}

// A staging env whose group shares production's postgres must not run its
// release Job: the Job inherits the env's mounts, so it would migrate the
// production database. The skip is reported as a succeeded outcome so the
// image still promotes, and no Job is created.
func TestRun_SkipsWhenNonProductionEnvMountsProductionDB(t *testing.T) {
	t.Parallel()
	r, cs := guardRunner(t, guardAddon("acme-db", "postgres", ""), guardAddon("acme-cache-staging", "redis", "staging"))
	env := guardEnv("staging", []string{"acme-db-conn", "acme-cache-staging-conn"})

	// Bounded: without the guard Run would create the Job and poll it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := r.Run(ctx, "kuso", env, &kube.KusoImage{Repository: "reg/acme/web", Tag: "abc"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != OutcomeSucceeded || !res.Skipped || res.Message == "" {
		t.Fatalf("want skipped succeeded outcome with a reason, got %+v", res)
	}
	jobs, _ := cs.BatchV1().Jobs("kuso").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Fatalf("release Job created against production's DB: %v", jobs.Items[0].Name)
	}
}

func TestProductionDataGuard(t *testing.T) {
	t.Parallel()
	r, _ := guardRunner(t,
		guardAddon("acme-db", "postgres", ""),
		guardAddon("acme-db-staging", "postgres", "staging"),
		guardAddon("acme-files", "s3", ""),
	)
	ref := kube.KusoEnvVar{Name: "DATABASE_URL", ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": "acme-db-conn", "key": "DATABASE_URL"},
	}}
	cases := []struct {
		name  string
		env   *kube.KusoEnvironment
		guard bool
	}{
		{"staging with fresh db and shared s3 runs", guardEnv("staging", []string{"acme-db-staging-conn", "acme-files-conn"}), false},
		{"staging mounting production db is skipped", guardEnv("staging", []string{"acme-db-conn"}), true},
		{"staging secretKeyRef to production db is skipped", guardEnv("staging", []string{"acme-db-staging-conn"}, ref), true},
		{"production env is never guarded", guardEnv("production", []string{"acme-db-conn"}), false},
	}
	for _, tc := range cases {
		reason, err := r.ProductionDataGuard(context.Background(), "kuso", tc.env)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (reason != "") != tc.guard {
			t.Errorf("%s: guard=%v, reason=%q", tc.name, tc.guard, reason)
		}
	}
}
