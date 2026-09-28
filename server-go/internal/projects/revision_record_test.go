package projects

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

type capturedRevision struct {
	project, kind, name, summary, actor string
	snapshot                            []byte
}

// captureRevisions installs a recorder stub on s. The actor is read from
// ctx the same way main.go's sink does, so a mutator that drops the
// caller's ctx shows up as an empty actor.
func captureRevisions(s *Service) *[]capturedRevision {
	var out []capturedRevision
	s.RecordRevision = func(ctx context.Context, project, kind, name, summary string, snapshot []byte) {
		out = append(out, capturedRevision{
			project: project, kind: kind, name: name, summary: summary,
			actor: auth.ActorName(ctx), snapshot: append([]byte(nil), snapshot...),
		})
	}
	return &out
}

func aliceCtx() context.Context {
	return auth.ContextWithClaims(context.Background(), &auth.Claims{Username: "alice"})
}

func managedSecret(project, service string, data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName(project, service), Namespace: "kuso"},
		Data:       d,
	}
}

func revisionFixture(t *testing.T, svcSpec kube.KusoServiceSpec, secrets ...runtime.Object) *Service {
	t.Helper()
	svcSpec.Project = "alpha"
	return fakeServiceWithSecrets(t, secrets,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", svcSpec),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)
}

func TestMutationsRecordOneRevision(t *testing.T) {
	t.Parallel()
	secretVal := "s3cr3t"
	cases := []struct {
		name     string
		spec     kube.KusoServiceSpec
		secrets  []runtime.Object
		run      func(ctx context.Context, s *Service) error
		kind     string
		resource string
		summary  string
	}{
		{
			name: "SetEnv",
			spec: kube.KusoServiceSpec{EnvVars: []kube.KusoEnvVar{{Name: "OLD", Value: "1"}, {Name: "KEEP", Value: "k"}}},
			run: func(ctx context.Context, s *Service) error {
				return s.SetEnv(ctx, "alpha", "web", []EnvVar{{Name: "KEEP", Value: "k"}, {Name: "FOO", Value: "a"}, {Name: "BAR", Value: "b"}})
			},
			kind: "service", resource: "web", summary: "env set BAR, FOO; env unset OLD",
		},
		{
			name: "SetEnvPending",
			run: func(ctx context.Context, s *Service) error {
				return s.SetEnvPending(ctx, "alpha", "web", []EnvVar{{Name: "FOO", Value: "a"}})
			},
			kind: "service", resource: "web", summary: "env set FOO",
		},
		{
			name: "SetEnvVar literal",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvVar(ctx, "alpha", "web", "FOO", SetEnvVarRequest{Value: "a"})
				return err
			},
			kind: "service", resource: "web", summary: "env set FOO",
		},
		{
			name: "SetEnvVar secret value",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvVar(ctx, "alpha", "web", "API_KEY", SetEnvVarRequest{SecretValue: &secretVal})
				return err
			},
			kind: "service", resource: "web", summary: "env set API_KEY (secret)",
		},
		{
			name: "SetEnvValue secret branch",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvValue(ctx, "alpha", "web", "API_KEY", secretVal)
				return err
			},
			kind: "service", resource: "web", summary: "env set API_KEY (secret)",
		},
		{
			name: "SetEnvValue build-relevant literal",
			spec: kube.KusoServiceSpec{PublicEnv: []string{"NEXT_PUBLIC_API_URL"}},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvValue(ctx, "alpha", "web", "NEXT_PUBLIC_API_URL", "https://api.example.com")
				return err
			},
			kind: "service", resource: "web", summary: "env set NEXT_PUBLIC_API_URL",
		},
		{
			name: "UnsetEnvVar literal",
			spec: kube.KusoServiceSpec{EnvVars: []kube.KusoEnvVar{{Name: "FOO", Value: "a"}}},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.UnsetEnvVar(ctx, "alpha", "web", "FOO")
				return err
			},
			kind: "service", resource: "web", summary: "env unset FOO",
		},
		{
			name:    "UnsetEnvVar secret",
			secrets: []runtime.Object{managedSecret("alpha", "web", map[string]string{"API_KEY": "x", "OTHER": "y"})},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.UnsetEnvVar(ctx, "alpha", "web", "API_KEY")
				return err
			},
			kind: "service", resource: "web", summary: "env unset API_KEY (secret)",
		},
		{
			name: "SetSubscribedAddons",
			spec: kube.KusoServiceSpec{SubscribedAddons: []string{"old"}},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetSubscribedAddons(ctx, "alpha", "web", []string{"pg"})
				return err
			},
			kind: "service", resource: "web", summary: "addon subscribe pg; addon unsubscribe old",
		},
		{
			name: "SetSharedEnvKeys",
			spec: kube.KusoServiceSpec{SharedEnvKeys: []string{"OLD_KEY"}},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"STRIPE_KEY", "JWT_SECRET"})
				return err
			},
			kind: "service", resource: "web", summary: "env share JWT_SECRET, STRIPE_KEY; env unshare OLD_KEY",
		},
		{
			name: "SetServiceBranchInEnv",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.Kube.CreateKusoEnvironment(context.Background(), "kuso", groupEnvCR("alpha", "web", "qa", "main"))
				if err != nil {
					return err
				}
				return s.SetServiceBranchInEnv(ctx, "alpha", "qa", "web", "feature-x")
			},
			kind: "environment", resource: "web-qa-production", summary: "env qa: branch main → feature-x",
		},
		{
			name: "AddDomain",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.AddDomain(ctx, "alpha", "web", AddDomainRequest{Host: "x.example.com", TLS: true})
				return err
			},
			kind: "service", resource: "web", summary: "domain add x.example.com",
		},
		{
			name: "RemoveDomain",
			spec: kube.KusoServiceSpec{Domains: []kube.KusoDomain{{Host: "x.example.com", TLS: true}}},
			run: func(ctx context.Context, s *Service) error {
				_, err := s.RemoveDomain(ctx, "alpha", "web", "x.example.com")
				return err
			},
			kind: "service", resource: "web", summary: "domain remove x.example.com",
		},
		{
			name: "RenameService",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.RenameService(ctx, "alpha", "web", "api")
				return err
			},
			kind: "service", resource: "api", summary: "rename web → api",
		},
		{
			name: "AddEnvironment",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "qa", Branch: "qa", ShareAddons: true})
				return err
			},
			kind: "environment", resource: "web-qa", summary: "env qa: create (branch qa)",
		},
		{
			name: "AddEnvDomain",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.AddEnvDomain(ctx, "alpha", "web", "staging", "s.example.com", "")
				return err
			},
			kind: "environment", resource: "web-staging", summary: "env staging: domain add s.example.com",
		},
		{
			name: "RemoveEnvDomain",
			run: func(ctx context.Context, s *Service) error {
				if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "s.example.com", ""); err != nil {
					return err
				}
				_, err := s.RemoveEnvDomain(ctx, "alpha", "web", "staging", "s.example.com")
				return err
			},
			kind: "environment", resource: "web-staging", summary: "env staging: domain remove s.example.com",
		},
		{
			name: "SetEnvDomains",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvDomains(ctx, "alpha", "web", "staging", []string{"b.example.com", "a.example.com"})
				return err
			},
			kind: "environment", resource: "web-staging", summary: "env staging: domain add a.example.com, b.example.com",
		},
		{
			name: "SetEnvScopedVar",
			run: func(ctx context.Context, s *Service) error {
				_, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "APP_ENV", SetEnvVarRequest{Value: "staging"})
				return err
			},
			kind: "environment", resource: "web-staging", summary: "env staging: env set APP_ENV",
		},
		{
			name: "UnsetEnvScopedVar",
			run: func(ctx context.Context, s *Service) error {
				if _, err := s.SetEnvScopedVar(context.Background(), "alpha", "web", "staging", "APP_ENV", SetEnvVarRequest{Value: "staging"}); err != nil {
					return err
				}
				_, err := s.UnsetEnvScopedVar(ctx, "alpha", "web", "staging", "APP_ENV")
				return err
			},
			kind: "environment", resource: "web-staging", summary: "env staging: env unset APP_ENV",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := revisionFixture(t, tc.spec, tc.secrets...)
			// Setup calls inside run use context.Background() so they can't
			// be mistaken for the mutation under test; filter them out by
			// actor below.
			got := captureRevisions(s)
			if err := tc.run(aliceCtx(), s); err != nil {
				t.Fatalf("mutation: %v", err)
			}
			var mine []capturedRevision
			for _, r := range *got {
				if r.actor == "alice" {
					mine = append(mine, r)
				}
			}
			if len(mine) != 1 {
				t.Fatalf("want exactly 1 revision by alice, got %d: %+v", len(mine), *got)
			}
			r := mine[0]
			if r.project != "alpha" || r.kind != tc.kind || r.name != tc.resource || r.summary != tc.summary {
				t.Fatalf("revision = %s/%s/%s %q, want alpha/%s/%s %q", r.project, r.kind, r.name, r.summary, tc.kind, tc.resource, tc.summary)
			}
			if !json.Valid(r.snapshot) {
				t.Fatalf("snapshot is not JSON: %s", r.snapshot)
			}
		})
	}
}

// A no-op list replace (kuso apply re-sending the same env) must not
// flood the history with empty revisions.
func TestSetEnvNoChangeRecordsNothing(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{EnvVars: []kube.KusoEnvVar{{Name: "FOO", Value: "a"}}})
	got := captureRevisions(s)
	if err := s.SetEnv(aliceCtx(), "alpha", "web", []EnvVar{{Name: "FOO", Value: "a"}}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("no-op SetEnv recorded %d revisions: %+v", len(*got), *got)
	}
}

func TestRevisionSnapshotsNeverCarrySecretValues(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	if err := s.SetEnv(ctx, "alpha", "web", []EnvVar{
		{Name: "API_TOKEN", Value: "tok-literal-123"},
		{Name: "PLAIN", Value: "hello-plain"},
	}); err != nil {
		t.Fatal(err)
	}
	sv := "managed-secret-456"
	if _, err := s.SetEnvVar(ctx, "alpha", "web", "WEBHOOK_SIGNING", SetEnvVarRequest{SecretValue: &sv}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnvValue(ctx, "alpha", "web", "OTHER", "unified-secret-789"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "DB_PASSWORD", SetEnvVarRequest{Value: "scoped-pw-000"}); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 4 {
		t.Fatalf("want 4 revisions, got %d", len(*got))
	}
	for _, r := range *got {
		for _, leak := range []string{"tok-literal-123", "managed-secret-456", "unified-secret-789", "scoped-pw-000"} {
			if strings.Contains(string(r.snapshot), leak) {
				t.Errorf("revision %q snapshot leaks %q: %s", r.summary, leak, r.snapshot)
			}
		}
	}
	// Non-secret literals stay so the revision can be replayed.
	if !strings.Contains(string((*got)[0].snapshot), "hello-plain") {
		t.Errorf("plain literal should be kept for replay: %s", (*got)[0].snapshot)
	}
}

func TestRevertServiceSnapshotReplaysEnvVars(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	if err := s.SetEnv(ctx, "alpha", "web", []EnvVar{{Name: "A", Value: "1"}, {Name: "API_TOKEN", Value: "old-tok"}}); err != nil {
		t.Fatal(err)
	}
	first := (*got)[0].snapshot
	if err := s.SetEnv(ctx, "alpha", "web", []EnvVar{{Name: "A", Value: "2"}, {Name: "B", Value: "3"}, {Name: "API_TOKEN", Value: "new-tok"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertServiceSnapshot(ctx, "alpha", "web", first); err != nil {
		t.Fatalf("revert: %v", err)
	}
	svc, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{}
	for _, e := range svc.Spec.EnvVars {
		vals[e.Name] = e.Value
	}
	// The masked secret keeps its current value: the revision never had it.
	want := map[string]string{"A": "1", "API_TOKEN": "new-tok"}
	if len(vals) != len(want) || vals["A"] != "1" || vals["API_TOKEN"] != "new-tok" {
		t.Fatalf("after revert env = %v, want %v", vals, want)
	}
	if len(*got) != 3 || (*got)[2].summary != "env set A; env unset B" {
		t.Fatalf("revert should record one revision describing the change, got %+v", *got)
	}
}

func TestRevertServiceSnapshotReplaysDomains(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	if _, err := s.AddDomain(ctx, "alpha", "web", AddDomainRequest{Host: "a.example.com", TLS: true}); err != nil {
		t.Fatal(err)
	}
	first := (*got)[0].snapshot
	if _, err := s.AddDomain(ctx, "alpha", "web", AddDomainRequest{Host: "b.example.com", TLS: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertServiceSnapshot(ctx, "alpha", "web", first); err != nil {
		t.Fatalf("revert: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if len(svc.Spec.Domains) != 1 || svc.Spec.Domains[0].Host != "a.example.com" {
		t.Fatalf("domains after revert = %+v", svc.Spec.Domains)
	}
	if last := (*got)[len(*got)-1]; len(*got) != 3 || last.summary != "domain remove b.example.com" {
		t.Fatalf("revert should record one revision, got %+v", *got)
	}
}

func TestRevertEnvironmentSnapshotReplaysDomainsAndOverrides(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	if _, err := s.AddEnvDomain(ctx, "alpha", "web", "staging", "a.example.com", ""); err != nil {
		t.Fatal(err)
	}
	domSnap := (*got)[0].snapshot
	if _, err := s.AddEnvDomain(ctx, "alpha", "web", "staging", "b.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertEnvironmentSnapshot(ctx, "alpha", "web-staging", domSnap); err != nil {
		t.Fatalf("revert domains: %v", err)
	}
	env, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-staging")
	if len(env.Spec.AdditionalHosts) != 1 || env.Spec.AdditionalHosts[0] != "a.example.com" {
		t.Fatalf("hosts after revert = %v", env.Spec.AdditionalHosts)
	}

	if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "APP_ENV", SetEnvVarRequest{Value: "one"}); err != nil {
		t.Fatal(err)
	}
	varSnap := (*got)[len(*got)-1].snapshot
	if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "APP_ENV", SetEnvVarRequest{Value: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "EXTRA", SetEnvVarRequest{Value: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertEnvironmentSnapshot(ctx, "alpha", "web-staging", varSnap); err != nil {
		t.Fatalf("revert overrides: %v", err)
	}
	env, _ = s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-staging")
	if v, _ := envVarValue(env, "APP_ENV"); v != "one" {
		t.Errorf("APP_ENV after revert = %q, want one", v)
	}
	if _, ok := envVarValue(env, "EXTRA"); ok {
		t.Error("EXTRA override should be gone after revert")
	}
}

func TestRevertRefusesInformationalRevisions(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	sv := "v"
	if _, err := s.SetEnvVar(ctx, "alpha", "web", "API_KEY", SetEnvVarRequest{SecretValue: &sv}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "qa", Branch: "qa", ShareAddons: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameService(ctx, "alpha", "web", "api"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 3 {
		t.Fatalf("want 3 revisions, got %+v", *got)
	}
	for i, r := range *got {
		if !RevisionInformational(r.snapshot) {
			t.Errorf("revision %q should be informational", r.summary)
		}
		var err error
		if r.kind == "environment" {
			err = s.RevertEnvironmentSnapshot(ctx, "alpha", r.name, r.snapshot)
		} else {
			err = s.RevertServiceSnapshot(ctx, "alpha", r.name, r.snapshot)
		}
		if !errors.Is(err, ErrNotRevertable) {
			t.Errorf("revision %d %q: revert err = %v, want ErrNotRevertable", i, r.summary, err)
		}
	}
	if len(*got) != 3 {
		t.Fatalf("refused reverts must not record revisions, got %+v", *got)
	}
}

// Rows written before this change are {"patch": <PatchServiceRequest>}.
func TestRevertServiceSnapshotAcceptsLegacyPatch(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{Port: 8080})
	if err := s.RevertServiceSnapshot(aliceCtx(), "alpha", "web", []byte(`{"patch":{"port":3000}}`)); err != nil {
		t.Fatalf("legacy revert: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if svc.Spec.Port != 3000 {
		t.Fatalf("port = %d, want 3000", svc.Spec.Port)
	}
	if RevisionInformational([]byte(`{"patch":{"port":3000}}`)) {
		t.Fatal("legacy patch rows are revertable")
	}
}

// groupEnvCR is an env-group clone's env: service "<svc>-<group>", CR name
// "<project>-<svc>-<group>-production", labelled env=<group>.
func groupEnvCR(project, svc, group, branch string) *kube.KusoEnvironment {
	cloneShort := svc + "-" + group
	return &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + cloneShort + "-production",
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: project, labelService: cloneShort, labelEnv: group},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project,
			Service: serviceCRName(project, cloneShort),
			Kind:    "production",
			Branch:  branch,
		},
	}
}

func TestRevertSharedEnvKeysSnapshot(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	got := captureRevisions(s)
	ctx := aliceCtx()
	if _, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	snap := (*got)[len(*got)-1].snapshot
	if _, err := s.SetSharedEnvKeys(ctx, "alpha", "web", []string{"C"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertServiceSnapshot(ctx, "alpha", "web", snap); err != nil {
		t.Fatalf("revert: %v", err)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "web")
	if strings.Join(svc.Spec.SharedEnvKeys, ",") != "A,B" {
		t.Fatalf("sharedEnvKeys after revert = %v, want [A B]", svc.Spec.SharedEnvKeys)
	}
}

func TestRevertEnvBranchSnapshot(t *testing.T) {
	t.Parallel()
	s := revisionFixture(t, kube.KusoServiceSpec{})
	if _, err := s.Kube.CreateKusoEnvironment(context.Background(), "kuso", groupEnvCR("alpha", "web", "qa", "main")); err != nil {
		t.Fatal(err)
	}
	got := captureRevisions(s)
	ctx := aliceCtx()
	if err := s.SetServiceBranchInEnv(ctx, "alpha", "qa", "web", "feature-x"); err != nil {
		t.Fatal(err)
	}
	rev := (*got)[len(*got)-1]
	if RevisionInformational(rev.snapshot) {
		t.Fatal("branch revision should be replayable")
	}
	if err := s.SetServiceBranchInEnv(ctx, "alpha", "qa", "web", "other"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertEnvironmentSnapshot(ctx, "alpha", rev.name, rev.snapshot); err != nil {
		t.Fatalf("revert: %v", err)
	}
	env, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-qa-production")
	if env.Spec.Branch != "feature-x" {
		t.Fatalf("branch after revert = %q, want feature-x", env.Spec.Branch)
	}
	last := (*got)[len(*got)-1]
	if last.summary != "env qa: branch other → feature-x" {
		t.Fatalf("replay revision summary = %q", last.summary)
	}
}
