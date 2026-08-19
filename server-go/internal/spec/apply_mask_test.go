package spec

// apply_mask_test.go covers the mask-sentinel guard on the declarative
// apply path. GET /spec masks literal env values ("••••••••") for
// callers without secrets:read; a client that round-trips that export
// through POST /apply must NOT end up with the literal mask stored as
// STRIPE_SECRET_KEY. Semantics under test:
//   - update + sentinel + stored literal  → resolved to the stored value
//   - update + sentinel + nothing stored  → env step fails, nothing written
//   - create + sentinel                   → create refused entirely
//   - no sentinel                         → no GetService round-trip needed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

var errNotFoundFake = errors.New("fake: service not found")

func TestApply_MaskedEnvOnUpdateResolvesToStoredValue(t *testing.T) {
	fp := &fakeProjects{
		existing: map[string]*kube.KusoService{
			"api": {Spec: kube.KusoServiceSpec{EnvVars: []kube.KusoEnvVar{
				{Name: "STRIPE_SECRET_KEY", Value: "sk_live_real"},
				{Name: "REF_BACKED", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "x", "key": "y"}}},
			}}},
		},
	}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Env: map[string]EnvValue{
				"STRIPE_SECRET_KEY": {Value: projects.EnvMaskSentinel},
				"PLAIN":             {Value: "hello"},
			},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected step errors: %+v", res.Errors)
	}
	if len(fp.envSet) != 1 {
		t.Fatalf("expected exactly one env write, got %d", len(fp.envSet))
	}
	got := map[string]string{}
	for _, ev := range fp.envSet[0].envVars {
		got[ev.Name] = ev.Value
	}
	if got["STRIPE_SECRET_KEY"] != "sk_live_real" {
		t.Errorf("sentinel not resolved to stored value: got %q", got["STRIPE_SECRET_KEY"])
	}
	if got["PLAIN"] != "hello" {
		t.Errorf("non-masked value mangled: got %q", got["PLAIN"])
	}
	for _, ev := range fp.envSet[0].envVars {
		if ev.Value == projects.EnvMaskSentinel {
			t.Errorf("literal mask sentinel written for %q", ev.Name)
		}
	}
}

func TestApply_MaskedEnvOnUpdateWithNoStoredValueFailsEnvStep(t *testing.T) {
	fp := &fakeProjects{
		existing: map[string]*kube.KusoService{
			// Stored service has NO literal for the masked key (one entry
			// is secretRef-backed — the export never masks those, so a
			// sentinel against it is a client error, not a keep).
			"api": {Spec: kube.KusoServiceSpec{EnvVars: []kube.KusoEnvVar{
				{Name: "NEW_KEY", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "x", "key": "y"}}},
			}}},
		},
	}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Env: map[string]EnvValue{
				"NEW_KEY": {Value: projects.EnvMaskSentinel},
				"OTHER":   {Value: "x"},
			},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Op != "env" {
		t.Fatalf("want exactly one env step error, got %+v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "NEW_KEY") {
		t.Errorf("error must name the offending key: %q", res.Errors[0].Message)
	}
	// The whole env write must be refused — a partial write (full
	// replace minus the masked key) would delete the stored key.
	if len(fp.envSet) != 0 {
		t.Fatalf("env must not be written on unresolved sentinel: %+v", fp.envSet)
	}
}

func TestApply_MaskedEnvOnCreateRefusesCreate(t *testing.T) {
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Env: map[string]EnvValue{
				"SECRET": {Value: projects.EnvMaskSentinel},
			},
		}},
	}
	plan := &Plan{ServicesToCreate: []string{"api"}}
	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Op != "create" {
		t.Fatalf("want exactly one create step error, got %+v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "SECRET") {
		t.Errorf("error must name the offending key: %q", res.Errors[0].Message)
	}
	if len(fp.created) != 0 {
		t.Fatalf("service must not be created with masked env: %+v", fp.created)
	}
	if len(fp.envSet) != 0 {
		t.Fatalf("env must not be written for a refused create: %+v", fp.envSet)
	}
}

func TestApply_NoSentinelSkipsGetServiceRoundTrip(t *testing.T) {
	// fp.existing is nil → GetService errors. A clean update (no
	// sentinel anywhere) must never call it, so the apply succeeds.
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	f := &File{
		Project: "shop",
		Services: []ServiceSpec{{
			Name: "api", Runtime: "dockerfile",
			Env: map[string]EnvValue{"PLAIN": {Value: "v"}},
		}},
	}
	plan := &Plan{ServicesToUpdate: []string{"api"}}
	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected step errors: %+v", res.Errors)
	}
	if len(fp.envSet) != 1 {
		t.Fatalf("env write missing: %+v", fp.envSet)
	}
}
