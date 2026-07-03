package projects

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

// setEnvHostsInSeed stamps spec.additionalHosts on a seeded env so a
// test can assert per-env custom domains are preserved across a
// service-level Domains save.
func setEnvHostsInSeed(s seed, hosts []string) error {
	vals := make([]any, len(hosts))
	for i, h := range hosts {
		vals[i] = h
	}
	return unstructured.SetNestedSlice(s.obj.Object, vals, "spec", "additionalHosts")
}

// PatchService → propagateChangedToEnvs is the single highest-leverage
// chokepoint in this package: the kusoenvironment chart reads ONLY the
// env CR, so any service-level field that doesn't mirror onto every
// owned env CR never reaches a running pod. These tests pin the
// load-bearing invariants the propagate.go comments flag as "keeps
// breaking in refactors" — exercised end-to-end through the public
// PatchService method against a fake apiserver.

// envByName lists the owned envs for (project, service) and returns
// them keyed by CR name, so assertions read the post-propagation
// state straight from the apiserver (not the in-memory svc copy).
func envByName(t *testing.T, s *Service, project, service string) map[string]kube.KusoEnvironment {
	t.Helper()
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(context.Background(), "kuso", map[string]string{
		labelProject: project,
		labelService: service,
	})
	if err != nil {
		t.Fatalf("list envs: %v", err)
	}
	out := make(map[string]kube.KusoEnvironment, len(envs))
	for i := range envs {
		out[envs[i].Name] = envs[i]
	}
	return out
}

// TestPatchService_PropagatesScalarFieldsToAllEnvs covers the common
// save flow: a Port/Runtime/Command/Resources edit on the service must
// land on every production-shaped env CR (production + staging here),
// because the chart renders each of those off the env CR.
func TestPatchService_PropagatesScalarFieldsToAllEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080, Runtime: "dockerfile"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	port := int32(3000)
	runtime := "worker"
	cmd := []string{"node", "worker.js"}
	resources := map[string]any{"requests": map[string]any{"cpu": "250m"}}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Port:      &port,
		Runtime:   &runtime,
		Command:   &cmd,
		Resources: &resources,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	envs := envByName(t, s, "alpha", "web")
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
	for name, env := range envs {
		if env.Spec.Port != 3000 {
			t.Errorf("%s: port = %d, want 3000", name, env.Spec.Port)
		}
		if env.Spec.Runtime != "worker" {
			t.Errorf("%s: runtime = %q, want worker", name, env.Spec.Runtime)
		}
		if len(env.Spec.Command) != 2 || env.Spec.Command[0] != "node" {
			t.Errorf("%s: command = %v, want [node worker.js]", name, env.Spec.Command)
		}
		if env.Spec.Resources == nil {
			t.Errorf("%s: resources not propagated", name)
		}
	}
}

// TestPropagate_SecurityContext pins service->env propagation of the
// per-service securityContext (Task 1's KusoSecurityContext): a
// service-level caps/escalation edit must reach every owned env CR so
// the chart re-renders the container securityContext. This patch also
// touches Resources so it walks the propagation loop regardless of
// whether SecurityContext alone flips a changed flag — see
// TestPropagate_SecurityContextOnly below for the isolated case, which
// is what actually exercises changedFields.SecurityContext.
func TestPropagate_SecurityContext(t *testing.T) {
	t.Parallel()
	esc := true
	sc := &kube.KusoSecurityContext{
		Capabilities:             &kube.KusoCapabilities{Add: []string{"SETUID", "SETGID"}},
		AllowPrivilegeEscalation: &esc,
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080, Runtime: "dockerfile"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	resources := map[string]any{"requests": map[string]any{"cpu": "250m"}}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Resources:       &resources,
		SecurityContext: sc,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	envs := envByName(t, s, "alpha", "web")
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
	for name, gotEnv := range envs {
		if !reflect.DeepEqual(gotEnv.Spec.SecurityContext, sc) {
			t.Errorf("%s: securityContext not propagated: got %+v want %+v", name, gotEnv.Spec.SecurityContext, sc)
		}
	}
}

// TestPropagate_SecurityContextOnly pins the actual bug found by the
// final review: a PatchService call whose ONLY change is
// securityContext must still walk propagateChangedToEnvs. Before the
// fix, setting req.SecurityContext alone did not flip any changedFields
// flag, so changed.any() was false, propagateChangedToEnvs
// early-returned, and the env CR (and thus the running pod) never saw
// the new securityContext — the primary UI surfaces for this feature
// (web Security section, `kuso project service set --cap-add`) were
// silently broken on an already-deployed service. This test sends a
// patch with ONLY SecurityContext set (no Resources, no other changed
// field) and asserts every owned env CR receives it.
func TestPropagate_SecurityContextOnly(t *testing.T) {
	t.Parallel()
	esc := true
	sc := &kube.KusoSecurityContext{
		Capabilities:             &kube.KusoCapabilities{Add: []string{"SETUID", "SETGID"}},
		AllowPrivilegeEscalation: &esc,
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080, Runtime: "dockerfile"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		SecurityContext: sc,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	envs := envByName(t, s, "alpha", "web")
	if len(envs) != 2 {
		t.Fatalf("expected 2 envs, got %d", len(envs))
	}
	for name, gotEnv := range envs {
		if !reflect.DeepEqual(gotEnv.Spec.SecurityContext, sc) {
			t.Errorf("%s: securityContext not propagated on securityContext-only patch: got %+v want %+v", name, gotEnv.Spec.SecurityContext, sc)
		}
	}
}

// (Scale-skips-preview is already covered by
// TestPatchService_ScaleDoesNotTouchPreviewEnvs in projects_test.go.)

// TestPatchService_DomainsDoesNotClobberPerEnvHosts pins the v0.16.19
// fix: custom domains are per-env (env.Spec.AdditionalHosts). A
// service-level Domains save must not overwrite an env's own custom
// hosts — otherwise a Networking save on production silently wipes
// staging's domains (or makes staging start serving production's host
// → Ingress conflict).
func TestPatchService_DomainsDoesNotClobberPerEnvHosts(t *testing.T) {
	t.Parallel()
	stagingSeed := seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging")
	if err := setEnvHostsInSeed(stagingSeed, []string{"staging.example.com"}); err != nil {
		t.Fatalf("seed staging hosts: %v", err)
	}
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		stagingSeed,
	)

	domains := []ServiceDomain{{Host: "prod.example.com"}}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Domains: &domains,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	envs := envByName(t, s, "alpha", "web")
	staging := envs["alpha-web-staging"]
	if len(staging.Spec.AdditionalHosts) != 1 || staging.Spec.AdditionalHosts[0] != "staging.example.com" {
		t.Errorf("staging AdditionalHosts = %v, want [staging.example.com] (must not be clobbered)", staging.Spec.AdditionalHosts)
	}
}

// TestPatchService_NoChangedFieldsSkipsEnvWrites pins the
// changedFields.any() short-circuit: a patch touching only the
// DisplayName (a service-only field) must not touch env CRs. We detect
// "no env write happened" by checking the resourceVersion is unchanged.
func TestPatchService_NoChangedFieldsSkipsEnvWrites(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	before := envByName(t, s, "alpha", "web")["alpha-web-production"]
	dn := "Web App"
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		DisplayName: &dn,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	after := envByName(t, s, "alpha", "web")["alpha-web-production"]
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("env CR resourceVersion changed (%s → %s) on a DisplayName-only patch — should be a no-op for envs",
			before.ResourceVersion, after.ResourceVersion)
	}
}
