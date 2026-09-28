package spec

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func managedSecret(project, service string, keys map[string]string, generated map[string]string) *corev1.Secret {
	data := map[string][]byte{}
	for k, v := range keys {
		data[k] = []byte(v)
	}
	ann := map[string]string{}
	for k, kind := range generated {
		ann["secrets.kuso.sislelabs.com/generated-"+k] = kind
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName(project, service), Namespace: "kuso", Annotations: ann},
		Data:       data,
	}
}

func TestParse_EnvSecretMarker(t *testing.T) {
	f, err := Parse([]byte("project: p\nservices:\n  - name: api\n    env:\n      FEATURE_X: {secret: true}\n      PLAIN: v\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ev := f.Services[0].Env["FEATURE_X"]
	if !ev.Secret || ev.Value != "" || ev.Generate != "" {
		t.Fatalf("FEATURE_X = %+v, want Secret", ev)
	}
	out, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(out), "secret: true") {
		t.Fatalf("secret marker not re-emitted:\n%s", out)
	}
	for _, bad := range []string{
		"{secret: true, value: x}",
		"{secret: true, generate: hex32}",
		"{secret: false}",
	} {
		if _, err := Parse([]byte("project: p\nservices:\n  - name: api\n    env:\n      K: " + bad + "\n")); err == nil {
			t.Errorf("Parse accepted %s", bad)
		}
	}
}

// #39: keys held only in the managed per-service Secret (the unified
// env write's default storage) export as {secret: true} — never with a
// value — instead of vanishing. Generated keys keep their {generate}.
func TestExport_ListsSecretHeldKeysWithoutValues(t *testing.T) {
	sec := managedSecret("rs", "api",
		map[string]string{"FEATURE_X": "on", "PAYLOAD_SECRET": "abc", "APP_ENV": "shadowed"},
		map[string]string{"PAYLOAD_SECRET": "hex32"})
	svc := typedPlanSeed(kube.GVRServices, "KusoService", "rs-api", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "rs-api", Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{Project: "rs", Runtime: "dockerfile", PublicEnv: []string{"APP_ENV"},
			EnvVars: []kube.KusoEnvVar{{Name: "APP_ENV", Value: "production"}}},
	})
	k, ns := fakeKubeWithSecrets(t, []*corev1.Secret{sec}, seedProject("rs"), svc)
	f, err := Export(context.Background(), k, ns, "rs")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	env := f.Services[0].Env
	want := map[string]EnvValue{
		"APP_ENV":        {Value: "production"}, // CR literal wins over the Secret copy
		"FEATURE_X":      {Secret: true},
		"PAYLOAD_SECRET": {Generate: "hex32"},
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %+v\nwant %+v", env, want)
	}
	out, _ := yaml.Marshal(f)
	if strings.Contains(string(out), "abc") || strings.Contains(string(out), ": on") {
		t.Fatalf("secret value leaked into export:\n%s", out)
	}

	// Round trip: the export re-plans to zero changes.
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	assertNoOpPlan(t, plan)
}

// {secret: true} is a no-op for apply: never written to the CR env
// (which would shadow the Secret), never cleared.
func TestApply_SecretMarkerIsNoOp(t *testing.T) {
	got := mapToEnvVars(map[string]EnvValue{"FEATURE_X": {Secret: true}, "PLAIN": {Value: "v"}})
	if len(got) != 1 || got[0].Name != "PLAIN" {
		t.Fatalf("mapToEnvVars must skip secret markers: %+v", got)
	}
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{Project: "rs", Services: []ServiceSpec{{Name: "api", Runtime: "dockerfile",
		Env: map[string]EnvValue{"FEATURE_X": {Secret: true}}}}}
	if _, err := r.Apply(context.Background(), &Plan{ServicesToCreate: []string{"api"}}, f, ApplyOpts{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(fp.created) != 1 || len(fp.created[0].req.EnvVars) != 0 {
		t.Fatalf("create must not carry the secret marker: %+v", fp.created)
	}
	if len(fp.envSet) != 0 {
		t.Fatalf("a create whose only env is a secret marker must not write env: %+v", fp.envSet)
	}
}

// A {secret: true} key the Secret doesn't hold can't be satisfied by
// apply — the plan warns so the user knows to set it out-of-band.
func TestPlanFor_WarnsOnMissingSecretKey(t *testing.T) {
	sec := managedSecret("rs", "api", map[string]string{"OTHER": "x"}, nil)
	k, ns := fakeKubeWithSecrets(t, []*corev1.Secret{sec}, seedProject("rs"), seedBareService("rs", "api"))
	f, err := Export(context.Background(), k, ns, "rs")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	f.Services[0].Env["FEATURE_X"] = EnvValue{Secret: true}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToUpdate) != 0 {
		t.Fatalf("a secret marker is never a change apply makes: %+v", plan.ServicesToUpdate)
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "FEATURE_X") {
		t.Fatalf("want one FEATURE_X warning, got %v", plan.Warnings)
	}
}
