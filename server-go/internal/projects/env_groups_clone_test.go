package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"kuso/server/internal/kube"
)

// TestCreateEnvGroup_ProvisionsInstanceAddonClone guards the bukvite
// env-group clone crash: when the source addon is instance-shared
// (spec.useInstanceAddon — a per-project DB on a shared server, NOT a
// StatefulSet), the fresh-clone loop writes the CR via CreateKusoAddon
// directly, which does NOT create the per-project DB or the <addon>-conn
// secret. The cloned service's DATABASE_URL secretKeyRef into that conn
// secret then fails (CreateContainerConfigError, 0/N ready). CreateEnvGroup
// must call ProvisionInstanceAddon for the fresh clone, passing the CLONED
// short name (short-<group>) and the source's UseInstanceAddon instance.
func TestCreateEnvGroup_ProvisionsInstanceAddonClone(t *testing.T) {
	t.Parallel()

	// Source addon is instance-shared: kind=postgres + UseInstanceAddon="pg".
	srcAddon := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-db",
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: "acme"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres", UseInstanceAddon: "pg"},
	})

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		srcAddon,
	)

	var got struct {
		project      string
		addonShort   string
		instanceName string
		calls        int
	}
	s.ProvisionInstanceAddon = func(ctx context.Context, project, addonShort, instanceName string) error {
		got.project = project
		got.addonShort = addonShort
		got.instanceName = instanceName
		got.calls++
		return nil
	}

	// No policy override → the "db" addon defaults to fresh, so it's cloned.
	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}

	if got.calls != 1 {
		t.Fatalf("ProvisionInstanceAddon called %d times, want 1", got.calls)
	}
	if got.project != "acme" {
		t.Errorf("ProvisionInstanceAddon project = %q, want %q", got.project, "acme")
	}
	// The cloned addon's short name is "<source-short>-<group>" = "db-staging".
	if got.addonShort != "db-staging" {
		t.Errorf("ProvisionInstanceAddon addonShort = %q, want %q (cloned short name)", got.addonShort, "db-staging")
	}
	// The shared instance the clone points at is inherited from the source's
	// spec.useInstanceAddon.
	if got.instanceName != "pg" {
		t.Errorf("ProvisionInstanceAddon instanceName = %q, want %q", got.instanceName, "pg")
	}
}

// TestCreateEnvGroup_RollbackCleansUpProvisionedInstanceAddon guards the
// orphan-leak fix: once ProvisionInstanceAddon has landed a per-project DB +
// login role + <addon>-conn secret on the shared server, a LATER-step failure
// (here a service-clone name conflict) must roll them back via
// CleanupInstanceAddon — otherwise a mid-clone abort leaks an empty DB + a live
// credential + an ownerless secret, unbounded across retries. Deleting the
// addon CR alone (raw DeleteKusoAddon) does NOT reclaim any of that.
func TestCreateEnvGroup_RollbackCleansUpProvisionedInstanceAddon(t *testing.T) {
	t.Parallel()

	// Source addon is instance-shared (spec.useInstanceAddon), so the fresh
	// clone loop calls ProvisionInstanceAddon.
	srcAddon := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-db",
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: "acme"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres", UseInstanceAddon: "pg"},
	})

	// Decoy service that COLLIDES by name with the clone target
	// ("acme-web-staging") so CreateKusoService fails AFTER the addon has been
	// provisioned. It carries env=other so it's excluded from the source-service
	// list (only production services are mirrored) and never buckets under the
	// "staging" group in the pre-existing-group conflict check.
	decoy := typedSeed(kube.GVRServices, "KusoService", "acme-web-staging", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-web-staging",
			Namespace: "kuso",
			Labels: map[string]string{
				labelProject: "acme",
				labelService: "web-staging",
				labelEnv:     "other",
			},
		},
		Spec: kube.KusoServiceSpec{Project: "acme", Port: 8080},
	})

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
		srcAddon,
		decoy,
	)

	var provisioned int
	s.ProvisionInstanceAddon = func(ctx context.Context, project, addonShort, instanceName string) error {
		provisioned++
		return nil
	}
	var cleanup struct {
		calls      int
		project    string
		addonShort string
	}
	s.CleanupInstanceAddon = func(ctx context.Context, project, addonShort string) error {
		cleanup.calls++
		cleanup.project = project
		cleanup.addonShort = addonShort
		return nil
	}

	// The service clone collides with the decoy → CreateEnvGroup fails AFTER
	// the instance addon was provisioned.
	_, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"})
	if err == nil {
		t.Fatal("CreateEnvGroup succeeded, want failure from service-clone conflict")
	}

	if provisioned != 1 {
		t.Fatalf("ProvisionInstanceAddon called %d times, want 1 (provision must land before the failure)", provisioned)
	}
	if cleanup.calls != 1 {
		t.Fatalf("CleanupInstanceAddon called %d times, want 1 (rollback must reclaim the provisioned instance addon)", cleanup.calls)
	}
	if cleanup.project != "acme" {
		t.Errorf("CleanupInstanceAddon project = %q, want %q", cleanup.project, "acme")
	}
	// Cleanup must target the CLONED short name ("db-staging"), the same name
	// ProvisionInstanceAddon was given.
	if cleanup.addonShort != "db-staging" {
		t.Errorf("CleanupInstanceAddon addonShort = %q, want %q (cloned short name)", cleanup.addonShort, "db-staging")
	}
}

// TestDeleteEnvGroup_CleansUpInstanceAddon guards the symmetric leak on
// teardown: DeleteEnvGroup raw-deletes the cloned addon CRs, which does NOT
// reclaim an instance-shared addon's per-project DB + role + conn secret. It
// must call CleanupInstanceAddon for each instance-shared addon in the group
// (while the CR still exists, so the instance can be resolved).
func TestDeleteEnvGroup_CleansUpInstanceAddon(t *testing.T) {
	t.Parallel()

	// A cloned instance-shared addon living in env-group "staging".
	cloned := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db-staging", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme-db-staging",
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: "acme", labelEnv: "staging"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres", UseInstanceAddon: "pg"},
	})

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		cloned,
	)

	var cleanup struct {
		calls      int
		addonShort string
	}
	s.CleanupInstanceAddon = func(ctx context.Context, project, addonShort string) error {
		cleanup.calls++
		cleanup.addonShort = addonShort
		return nil
	}

	if err := s.DeleteEnvGroup(context.Background(), "acme", "staging"); err != nil {
		t.Fatalf("DeleteEnvGroup: %v", err)
	}
	if cleanup.calls != 1 {
		t.Fatalf("CleanupInstanceAddon called %d times, want 1", cleanup.calls)
	}
	if cleanup.addonShort != "db-staging" {
		t.Errorf("CleanupInstanceAddon addonShort = %q, want %q", cleanup.addonShort, "db-staging")
	}
}

// TestCreateEnvGroup_DoesNotInheritCustomDomains guards against the
// traffic-hijack bug where a cloned env stamped the SOURCE service's
// production custom domains into its own AdditionalHosts/TLSHosts (TLS on).
// The clone's Ingress would then claim production's host and race it for
// the same Let's Encrypt cert. The single-env / preview path NILs these;
// the clone path must too. Custom domains on a cloned env are an explicit
// opt-in via `kuso domains add`, never silent inheritance.
func TestCreateEnvGroup_DoesNotInheritCustomDomains(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{
			Port: 8080,
			Domains: []kube.KusoDomain{
				{Host: "www.acme.com", TLS: true},
				{Host: "acme.com", TLS: true},
			},
		}),
		// Production env for the source service so the clone can inherit
		// the deployed image (path exercised in CreateEnvGroup).
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)

	summary, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{
		Name: "staging",
	})
	if err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	if summary == nil {
		t.Fatal("nil summary")
	}

	// The cloned env CR is named "<project>-<short>-<group>-production".
	clonedEnvName := "acme-web-staging-production"
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", clonedEnvName)
	if err != nil {
		t.Fatalf("get cloned env %s: %v", clonedEnvName, err)
	}

	if len(env.Spec.AdditionalHosts) != 0 {
		t.Errorf("cloned env inherited source custom domains: AdditionalHosts = %v (want none)", env.Spec.AdditionalHosts)
	}
	// TLSHosts must only cover the clone's own generated host, never the
	// source service's custom domains.
	for _, h := range env.Spec.TLSHosts {
		if h == "www.acme.com" || h == "acme.com" {
			t.Errorf("cloned env TLSHosts leaked source custom domain %q: %v", h, env.Spec.TLSHosts)
		}
	}
}

// TestCreateEnvGroup_CarriesServiceSecrets guards against the clone
// dropping the service's OWN managed secrets. Production mounts
// <project>-<service>-secrets (app config/keys) via envFromSecrets; a
// clone that omitted it booted without app config and crash-looped at
// 0/N ready (the bukvite staging incident). The clone must carry the
// service-level secret + the env-scoped secret for the new env.
func TestCreateEnvGroup_CarriesServiceSecrets(t *testing.T) {
	t.Parallel()

	// Source service has a managed app-config secret (acme-web-secrets).
	srcSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName("acme", "web"), Namespace: "kuso"},
		Data:       map[string][]byte{"APP_KEY": []byte("s3cr3t")},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{srcSecret},
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}

	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-web-staging-production")
	if err != nil {
		t.Fatalf("get cloned env: %v", err)
	}

	has := func(name string) bool {
		for _, s := range env.Spec.EnvFromSecrets {
			if s == name {
				return true
			}
		}
		return false
	}
	// The clone must mount its OWN label-consistent managed secret
	// (acme-web-staging-secrets), NOT the source's acme-web-secrets — the
	// label-derived name is what RefreshEnvSecrets recomputes, so mounting
	// the source name would be dropped on the next addon churn.
	wantSvc := kube.ServiceSecretName("acme", "web-staging")
	if !has(wantSvc) {
		t.Errorf("cloned env missing label-consistent service secret %q: %v", wantSvc, env.Spec.EnvFromSecrets)
	}
	if has(kube.ServiceSecretName("acme", "web")) {
		t.Errorf("cloned env mounts the SOURCE secret %q (label-mismatched, will be dropped by RefreshEnvSecrets): %v",
			kube.ServiceSecretName("acme", "web"), env.Spec.EnvFromSecrets)
	}
	// And the source secret's contents must have been COPIED into the
	// clone's own secret (isolated staging config).
	copied, err := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), wantSvc, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("clone secret %s not created: %v", wantSvc, err)
	}
	if string(copied.Data["APP_KEY"]) != "s3cr3t" {
		t.Errorf("clone secret did not copy source data: %v", copied.Data)
	}
}

// TestCreateEnvGroup_ClonedServiceCarriesServiceLabel guards the production-
// canvas duplicate bug: the cloned KusoService MUST carry the
// kuso.sislelabs.com/service label (=its own short name), or the canvas +
// resolvers can't scope it to its env and it leaks into the production view.
func TestCreateEnvGroup_ClonedServiceCarriesServiceLabel(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)
	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	svc, err := s.Kube.GetKusoService(context.Background(), "kuso", "acme-web-staging")
	if err != nil {
		t.Fatalf("get cloned service: %v", err)
	}
	if got := svc.Labels[kube.LabelService]; got != "web-staging" {
		t.Errorf("cloned service label kuso.sislelabs.com/service = %q, want web-staging (unlabeled -> leaks into prod canvas): labels=%v", got, svc.Labels)
	}
	if svc.Labels[kube.LabelEnv] != "staging" {
		t.Errorf("cloned service env label = %q, want staging", svc.Labels[kube.LabelEnv])
	}
}

// TestCreateEnvGroup_CloneDropsDomainsAndSuffixesDisplayName guards the
// bukvite confusion (2026-08-12): the service clone copied production's
// spec verbatim, so the staging service carried production's custom
// domains (a re-armed traffic-hijack hazard — any domains propagation
// on the clone would push prod hostnames onto the staging env) and an
// identical displayName (canvas node + Discord build cards for staging
// were indistinguishable from production's).
func TestCreateEnvGroup_CloneDropsDomainsAndSuffixesDisplayName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "web", kube.KusoServiceSpec{
			Project:     "acme",
			Port:        8080,
			DisplayName: "webby",
			Domains: []kube.KusoDomain{
				{Host: "acme.example.com", TLS: true},
				{Host: "www.acme.example.com", TLS: true},
			},
		}),
		seedEnv("acme", "web", "production", "main", "acme-web-production"),
	)

	if _, err := s.CreateEnvGroup(context.Background(), "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	clone, err := s.Kube.GetKusoService(context.Background(), "kuso", "acme-web-staging")
	if err != nil {
		t.Fatalf("get clone: %v", err)
	}
	if len(clone.Spec.Domains) != 0 {
		t.Errorf("clone inherited custom domains: %+v", clone.Spec.Domains)
	}
	if clone.Spec.DisplayName != "webby-staging" {
		t.Errorf("clone displayName = %q, want %q", clone.Spec.DisplayName, "webby-staging")
	}
	// The source must be untouched.
	src, _ := s.Kube.GetKusoService(context.Background(), "kuso", "acme-web")
	if len(src.Spec.Domains) != 2 || src.Spec.DisplayName != "webby" {
		t.Errorf("source service mutated: displayName=%q domains=%+v", src.Spec.DisplayName, src.Spec.Domains)
	}
}
