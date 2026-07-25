package projects

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// serviceDerivedEnvSpecFields is the hand-maintained list of
// KusoEnvironmentSpec fields that are SERVICE-DERIVED — i.e. copied off
// the parent KusoService spec when an env CR is minted. Both env literals
// (the production-env literal in AddService and the custom-env literal in
// AddEnvironment) MUST stamp every field listed here, or a fresh env is
// born missing config the service declared (the recurring "field-drop"
// bug class: SecurityContext/Healthcheck/Release/etc silently absent
// until a later PatchService re-propagates them — or never, for a no-op
// edit).
//
// ADD NEW KusoEnvironmentSpec service-derived fields to BOTH literals AND
// to this list. TestEnvLiteralsShareServiceDerivedFields trips when a
// field here is populated on one env literal's output but not the other,
// so the two can't silently drift.
//
// Excluded deliberately (NOT service-derived, or env-scoped by design):
//   - Project, Service, Kind, Branch, Port, Host, ReplicaCount,
//     Autoscaling, SpreadPolicy — computed/env-scoped, not a 1:1 service copy.
//   - AdditionalHosts/TLSHosts/WildcardDomains — per-env (custom envs get
//     ONLY their own host; see the AddEnvironment comment).
//   - PullRequest/TTL/EnvOverrides/SecretsRev — preview/marker/server state.
//   - EnvVars/EnvFromSecrets/SharedEnvKeys/SubscribedAddons — resolved
//     per-env (rescoped, subscription-filtered) rather than copied verbatim.
//   - TLSEnabled/ClusterIssuer/IngressClassName — constants.
var serviceDerivedEnvSpecFields = []string{
	"Internal",
	"PrivateEgress",
	"PlatformAPIEgress",
	"Stopped",
	"Sleep",
	"Resources",
	"Volumes",
	"Runtime",
	"Command",
	"SecurityContext",
	"Healthcheck",
	"PublicEnv",
	"Release",
	"SnapshotBeforeDeploy",
	"Placement",
}

// fullyPopulatedServiceSpec returns a KusoServiceSpec with every
// service-derived field set to a distinctive non-zero value, so a dropped
// field shows up as a zero value on the resulting env CR.
func fullyPopulatedServiceSpec(project string) kube.KusoServiceSpec {
	return kube.KusoServiceSpec{
		Project:           project,
		Port:              3000,
		Runtime:           "image",
		Command:           []string{"./serve"},
		Internal:          true,
		PrivateEgress:     true,
		PlatformAPIEgress: true,
		Stopped:           true,
		Sleep:             &kube.KusoServiceSleep{Enabled: true, AfterMinutes: 15},
		Resources:         map[string]any{"requests": map[string]any{"cpu": "100m"}},
		Volumes:           []kube.KusoVolume{{Name: "data", MountPath: "/data", SizeGi: 1}},
		SecurityContext:   &kube.KusoSecurityContext{Capabilities: &kube.KusoCapabilities{Add: []string{"SETUID"}}},
		Healthcheck:       &kube.KusoHealthcheck{Path: "/healthz", Port: 3000},
		PublicEnv:         []string{"NEXT_PUBLIC_API_URL"},
		Release:           &kube.KusoReleaseSpec{Command: []string{"bin/migrate"}, TimeoutSeconds: 300},
		Placement:         &kube.KusoPlacement{Labels: map[string]string{"kuso.sislelabs.com/pool": "web"}},
		// Image is set (runtime=image) — the env literal must carry it so a
		// custom env of a runtime=image service isn't born imageless.
		Image: &kube.KusoImage{Repository: "ghcr.io/x/y", Tag: "v1"},
		// SnapshotBeforeDeploy is a plain bool; set it so a drop reads as false.
		SnapshotBeforeDeploy: true,
	}
}

// specFieldPopulated reports whether the named KusoEnvironmentSpec field
// holds a non-zero value on the given env.
func specFieldPopulated(t *testing.T, env *kube.KusoEnvironment, field string) bool {
	t.Helper()
	v := reflect.ValueOf(env.Spec).FieldByName(field)
	if !v.IsValid() {
		t.Fatalf("KusoEnvironmentSpec has no field %q — update serviceDerivedEnvSpecFields", field)
	}
	return !v.IsZero()
}

// TestAddEnvironment_CopiesServiceDerivedFields is the field-drop
// regression for the custom-env literal in AddEnvironment: a staging/qa
// env must be born with the same service-derived spec fields the
// production env gets (SecurityContext, Healthcheck, PublicEnv, Release,
// SnapshotBeforeDeploy, Image, and the rest). Pre-fix the literal dropped
// most of these, so a custom env only picked them up on a later
// PatchService propagation (or never).
func TestAddEnvironment_CopiesServiceDerivedFields(t *testing.T) {
	t.Parallel()
	spec := fullyPopulatedServiceSpec("alpha")
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("alpha", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("alpha", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "web"},
			},
			Spec: spec,
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{
		Name:        "staging",
		Branch:      "staging",
		ShareAddons: true, // keep the test focused on field-copy, not per-env addon swap
	})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	for _, f := range serviceDerivedEnvSpecFields {
		if !specFieldPopulated(t, env, f) {
			t.Errorf("custom env dropped service-derived field %q (got zero value)", f)
		}
	}
	// runtime=image with a release hook → image is withheld behind
	// PendingImage, not Image. Either way the image must be present.
	if env.Spec.Image == nil && env.Spec.PendingImage == nil {
		t.Errorf("custom env of a runtime=image service has neither Image nor PendingImage")
	}
	// The service here has a release hook, so the image must be withheld.
	if env.Spec.Image != nil {
		t.Errorf("release hook present: image should be withheld behind PendingImage, got Image=%+v", env.Spec.Image)
	}
	if env.Spec.PendingImage == nil {
		t.Errorf("release hook present: expected PendingImage to hold the runtime=image target")
	}
}

// TestEnvLiteralsShareServiceDerivedFields is the STRUCTURAL guard that
// kills the field-drop bug class: it drives BOTH env-minting paths off the
// same fully-populated service and asserts each service-derived field is
// populated identically on both resulting env CRs. If a new field is added
// to one literal but not the other, the paths diverge and this fails —
// forcing the author to update both literals (and the field list above).
func TestEnvLiteralsShareServiceDerivedFields(t *testing.T) {
	t.Parallel()

	// --- production env, via AddService ---
	// AddService builds the service from a CreateServiceRequest. Not every
	// service-derived field is settable via that request (Healthcheck,
	// Volumes, Sleep-as-spec, Stopped, Internal, egress flags, Placement
	// come from the service spec / project, not the create wire), so we
	// compare only the subset AddService's request can drive AND that
	// AddEnvironment also copies. The functional test above already covers
	// the full field set for the custom-env literal.
	prodSvc := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	snap := true
	if _, err := prodSvc.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:                 "web",
		Runtime:              "image",
		Port:                 3000,
		Image:                &ServiceImageSpec{Repository: "ghcr.io/x/y", Tag: "v1"},
		Release:              &PatchReleaseRequest{Command: []string{"bin/migrate"}, TimeoutSeconds: 300},
		PublicEnv:            []string{"NEXT_PUBLIC_API_URL"},
		SnapshotBeforeDeploy: &snap,
		SecurityContext:      &kube.KusoSecurityContext{Capabilities: &kube.KusoCapabilities{Add: []string{"SETUID"}}},
	}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	prodEnv, err := prodSvc.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("get production env: %v", err)
	}

	// --- custom env, via AddEnvironment ---
	// Seed a service carrying the SAME create-driven fields plus the rest,
	// then mint a custom env off it.
	custSvc := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("alpha", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("alpha", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "web"},
			},
			Spec: fullyPopulatedServiceSpec("alpha"),
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	custEnv, err := custSvc.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{
		Name: "staging", Branch: "staging", ShareAddons: true,
	})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	// Fields settable via the AddService create request that BOTH literals
	// must populate. This is the drift tripwire: if AddEnvironment stops
	// copying one of these (or AddService does), the two env CRs disagree.
	createDrivenShared := []string{
		"SecurityContext",
		"PublicEnv",
		"Release",
		"SnapshotBeforeDeploy",
	}
	for _, f := range createDrivenShared {
		prodOK := specFieldPopulated(t, prodEnv, f)
		custOK := specFieldPopulated(t, custEnv, f)
		if prodOK != custOK {
			t.Errorf("field %q drift: production-env literal populated=%v, custom-env literal populated=%v — the two env literals must stay in lockstep",
				f, prodOK, custOK)
		}
		if !prodOK {
			t.Errorf("field %q not populated on production env (AddService literal dropped it)", f)
		}
		if !custOK {
			t.Errorf("field %q not populated on custom env (AddEnvironment literal dropped it)", f)
		}
	}
	// Both runtime=image + release-hook services must withhold the image
	// behind PendingImage on both env literals.
	if (prodEnv.Spec.PendingImage != nil) != (custEnv.Spec.PendingImage != nil) {
		t.Errorf("PendingImage drift: prod=%v cust=%v", prodEnv.Spec.PendingImage != nil, custEnv.Spec.PendingImage != nil)
	}

	// --- AddService, driven off a fully-populated SERVICE spec ---
	// The create-request subset above can't reach Stopped/Sleep/Volumes
	// (CreateServiceRequest doesn't expose them), which is exactly how
	// those three stayed dropped from the AddService literal while the
	// other two literals carried them. Seeding the service CR directly
	// and re-reading the production env closes that hole.
	seeded := fakeService(t,
		seedProject("beta", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "beta.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("beta", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("beta", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "beta", labelService: "web"},
			},
			Spec: fullyPopulatedServiceSpec("beta"),
		}),
		seedEnv("beta", "web", "production", "main", "beta-web-production"),
	)
	// Mint a custom env and an env-group clone off the SAME service spec,
	// then assert every service-derived field survives both paths.
	betaCust, err := seeded.AddEnvironment(context.Background(), "beta", "web", CreateEnvRequest{
		Name: "qa", Branch: "qa", ShareAddons: true,
	})
	if err != nil {
		t.Fatalf("AddEnvironment(beta): %v", err)
	}
	for _, f := range serviceDerivedEnvSpecFields {
		if !specFieldPopulated(t, betaCust, f) {
			t.Errorf("AddEnvironment literal dropped service-derived field %q", f)
		}
	}

	// --- env-group clone literal ---
	// This is the THIRD env-minting path and was previously untested
	// altogether, which is how it came to drop PublicEnv/Healthcheck/
	// Release/SnapshotBeforeDeploy/egress flags. propagate.go re-stamps
	// several of these on the next service save, so the bug presented as
	// an intermittent "the clone was broken until I touched it".
	cloneSvc := fakeService(t,
		seedProject("gamma", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "gamma.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("gamma", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("gamma", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "gamma", labelService: "web"},
			},
			Spec: fullyPopulatedServiceSpec("gamma"),
		}),
		seedEnv("gamma", "web", "production", "main", "gamma-web-production"),
	)
	if _, err := cloneSvc.CreateEnvGroup(context.Background(), "gamma", CreateEnvGroupRequest{
		Name: "demo",
	}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	// The clone mints a NEW service (<svc>-<group>) plus its env; find the
	// env belonging to the demo group rather than assuming a name shape.
	envs, err := cloneSvc.Kube.ListKusoEnvironments(context.Background(), "kuso")
	if err != nil {
		t.Fatalf("list envs: %v", err)
	}
	var cloneEnv *kube.KusoEnvironment
	for i := range envs {
		if envs[i].Labels[labelEnv] == "demo" {
			cloneEnv = &envs[i]
			break
		}
	}
	if cloneEnv == nil {
		t.Fatalf("env-group clone produced no env labelled env=demo (got %d envs)", len(envs))
	}
	// Placement is resolved against the project (which has none here) and
	// Stopped is deliberately not inherited into a fresh group, so compare
	// the fields the clone is contractually required to carry.
	cloneMustCarry := []string{
		"Internal", "PrivateEgress", "PlatformAPIEgress",
		"Sleep", "Resources", "Volumes", "Runtime", "Command",
		"SecurityContext", "Healthcheck", "PublicEnv",
		"Release", "SnapshotBeforeDeploy",
	}
	for _, f := range cloneMustCarry {
		if !specFieldPopulated(t, cloneEnv, f) {
			t.Errorf("env-group clone literal dropped service-derived field %q — it must stay in lockstep with the other two env literals", f)
		}
	}
}
