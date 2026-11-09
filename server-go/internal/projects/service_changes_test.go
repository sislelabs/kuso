package projects

import (
	"context"
	"testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// envWrites counts update/patch calls against KusoEnvironment CRs; each
// one is a helm-operator reconcile and, for podspec fields, a rollout.
func envWrites(s *Service) map[string]int {
	out := map[string]int{}
	for _, a := range s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).Actions() {
		if a.GetResource() != kube.GVREnvironments {
			continue
		}
		switch act := a.(type) {
		case k8stesting.UpdateAction:
			out[act.GetObject().(interface{ GetName() string }).GetName()]++
		case k8stesting.PatchAction:
			out[act.GetName()]++
		}
	}
	return out
}

func stagedFixture(t *testing.T) *Service {
	t.Helper()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "main", "alpha-web-staging"),
	)
	// Steady state: the managed Secret is already mounted (heal sweep /
	// first write), so attaching it is a no-op during the batch.
	ns := "kuso"
	if err := s.upsertManagedSecretKey(context.Background(), ns, "alpha", "web", "SEED", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.attachServiceSecretToEnvs(context.Background(), ns, "alpha", "web"); err != nil {
		t.Fatal(err)
	}
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).ClearActions()
	return s
}

func TestApplyServiceChanges_WritesEachEnvOnce(t *testing.T) {
	s := stagedFixture(t)
	port := int32(9090)
	res, err := s.ApplyServiceChanges(context.Background(), "alpha", "web", ServiceChangesRequest{
		EnvSet:   []ServiceChangeEnvSet{{Name: "API_KEY", Value: "a"}, {Name: "OTHER", Value: "b"}},
		EnvUnset: []string{"SEED"},
		Patch:    &PatchServiceRequest{Port: &port},
	})
	if err != nil {
		t.Fatalf("ApplyServiceChanges: %v", err)
	}
	for _, r := range res.Results {
		if !r.OK {
			t.Fatalf("item %s %s failed: %s", r.Kind, r.Name, r.Error)
		}
	}
	if !res.RolledOut || res.FlushError != "" {
		t.Fatalf("rolledOut=%v flushError=%q", res.RolledOut, res.FlushError)
	}
	writes := envWrites(s)
	for _, env := range []string{"alpha-web-production", "alpha-web-staging"} {
		if writes[env] != 1 {
			t.Errorf("%s written %d times, want exactly 1 (writes: %v)", env, writes[env], writes)
		}
		got, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", env)
		if err != nil {
			t.Fatal(err)
		}
		if got.Spec.Port != 9090 {
			t.Errorf("%s port = %d, want 9090", env, got.Spec.Port)
		}
		if got.Spec.SecretsRev == "" {
			t.Errorf("%s secretsRev not stamped: secret edits would never reach the pods", env)
		}
	}
	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if string(sec.Data["API_KEY"]) != "a" || string(sec.Data["OTHER"]) != "b" {
		t.Errorf("managed secret = %v", sec.Data)
	}
	if _, ok := sec.Data["SEED"]; ok {
		t.Error("SEED should have been removed")
	}
}

// A failing item leaves the rest applied and still rolls them out once.
func TestApplyServiceChanges_PartialFailure(t *testing.T) {
	s := stagedFixture(t)
	res, err := s.ApplyServiceChanges(context.Background(), "alpha", "web", ServiceChangesRequest{
		EnvSet: []ServiceChangeEnvSet{{Name: "1BAD", Value: "x"}, {Name: "GOOD", Value: "y"}},
	})
	if err != nil {
		t.Fatalf("ApplyServiceChanges: %v", err)
	}
	if len(res.Results) != 2 || res.Results[0].OK || !res.Results[1].OK {
		t.Fatalf("results = %+v, want first failed, second ok", res.Results)
	}
	if !res.RolledOut {
		t.Fatal("the good write should still roll out")
	}
	if w := envWrites(s)["alpha-web-production"]; w != 1 {
		t.Errorf("production written %d times, want 1", w)
	}
}

func TestApplyServiceChanges_Empty(t *testing.T) {
	s := stagedFixture(t)
	if _, err := s.ApplyServiceChanges(context.Background(), "alpha", "web", ServiceChangesRequest{}); err == nil {
		t.Fatal("want an error for an empty batch")
	}
}
