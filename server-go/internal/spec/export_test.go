package spec

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedFullService seeds a KusoService CR with a representative field
// set so the round-trip exercises the non-trivial mappings.
func seedFullService(project, service string) planSeed {
	name := project + "-" + service
	return typedPlanSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project:       project,
			Runtime:       "dockerfile",
			Port:          8080,
			PrivateEgress: true,
			Repo:          &kube.KusoRepoRef{URL: "https://github.com/me/api", DefaultBranch: "main"},
			Domains:       []kube.KusoDomain{{Host: "api.shop.example.com", TLS: true}},
			Scale:         &kube.KusoScaleSpec{Min: 2, Max: 6, TargetCPU: 65},
			Sleep:         &kube.KusoServiceSleep{Enabled: true, AfterMinutes: 20},
			Placement:     &kube.KusoPlacement{Labels: map[string]string{"region": "eu"}},
			Volumes:       []kube.KusoVolume{{Name: "data", MountPath: "/data", SizeGi: 5}},
			EnvVars: []kube.KusoEnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{
						"name": project + "-db-conn",
						"key":  "DATABASE_URL",
					},
				}},
			},
		},
	})
}

func seedFullAddon(project, addon string) planSeed {
	name := project + "-" + addon
	return typedPlanSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoAddonSpec{
			Project: project,
			Kind:    "postgres",
			Version: "16",
			HA:      true,
			Pooler:  &kube.KusoAddonPooler{Enabled: true},
			Backup:  &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 7},
		},
	})
}

func seedFullCron(project, cron string) planSeed {
	name := project + "-" + cron
	return typedPlanSeed(kube.GVRCrons, "KusoCron", name, &kube.KusoCron{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoCronSpec{
			Project:  project,
			Kind:     "http",
			Schedule: "0 2 * * *",
			URL:      "https://x",
		},
	})
}

func seedProject(project string) planSeed {
	return typedPlanSeed(kube.GVRProjects, "KusoProject", project, &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: project, Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{BaseDomain: "shop.example.com"},
	})
}

func TestExport_RoundTripsToNoOpPlan(t *testing.T) {
	k, ns := fakeKube(t,
		seedProject("shop"),
		seedFullService("shop", "api"),
		seedFullAddon("shop", "db"),
		seedFullCron("shop", "nightly"),
	)

	f, err := Export(context.Background(), k, ns, "shop")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if f.Project != "shop" {
		t.Fatalf("project wrong: %+v", f)
	}
	if f.APIVersion != "kuso/v1" {
		t.Fatalf("apiVersion not set: %q", f.APIVersion)
	}
	if f.Prune {
		t.Fatalf("export must not set prune")
	}
	if len(f.Services) != 1 || f.Services[0].Name != "api" {
		t.Fatalf("service not exported: %+v", f.Services)
	}
	s := f.Services[0]
	if s.Runtime != "dockerfile" || s.Port != 8080 || !s.PrivateEgress {
		t.Fatalf("service scalars not exported: %+v", s)
	}
	if s.Repo != "https://github.com/me/api" || s.Branch != "main" {
		t.Fatalf("service repo not exported: %+v", s)
	}
	if s.Sleep == nil || !s.Sleep.Enabled || s.Sleep.AfterMinutes != 20 {
		t.Fatalf("service sleep not exported: %+v", s.Sleep)
	}
	if s.Placement == nil || s.Placement.Labels["region"] != "eu" {
		t.Fatalf("service placement not exported: %+v", s.Placement)
	}
	if s.Scale == nil || s.Scale.Min != 2 || s.Scale.Max != 6 || s.Scale.TargetCPU != 65 {
		t.Fatalf("service scale not exported: %+v", s.Scale)
	}
	if len(s.Domains) != 1 || s.Domains[0].Host != "api.shop.example.com" || !s.Domains[0].TLS {
		t.Fatalf("service domains not exported: %+v", s.Domains)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].Name != "data" || s.Volumes[0].SizeGi != 5 {
		t.Fatalf("service volumes not exported: %+v", s.Volumes)
	}
	if s.Env["LOG_LEVEL"] != "info" {
		t.Fatalf("plain env not exported: %+v", s.Env)
	}
	if s.Env["DATABASE_URL"] != "${{ db.DATABASE_URL }}" {
		t.Fatalf("addon-conn valueFrom not reversed: %q", s.Env["DATABASE_URL"])
	}

	if len(f.Addons) != 1 || f.Addons[0].Name != "db" {
		t.Fatalf("addon not exported: %+v", f.Addons)
	}
	a := f.Addons[0]
	if a.Kind != "postgres" || a.Version != "16" || !a.HA {
		t.Fatalf("addon scalars not exported: %+v", a)
	}
	if a.Pooler == nil || !a.Pooler.Enabled {
		t.Fatalf("addon pooler not exported: %+v", a.Pooler)
	}
	if a.Backup == nil || a.Backup.Schedule != "0 3 * * *" || a.Backup.RetentionDays != 7 {
		t.Fatalf("addon backup not exported: %+v", a.Backup)
	}

	if len(f.Crons) != 1 || f.Crons[0].Name != "nightly" {
		t.Fatalf("cron not exported: %+v", f.Crons)
	}
	c := f.Crons[0]
	if c.Kind != "http" || c.Schedule != "0 2 * * *" || c.URL != "https://x" {
		t.Fatalf("cron fields not exported: %+v", c)
	}

	// The exported File must parse and re-plan to a no-op against the
	// same live state — proving export is faithful.
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.ServicesToCreate)+len(plan.ServicesToDelete) != 0 {
		t.Fatalf("export did not round-trip to a no-op service plan: %+v", plan)
	}
	if len(plan.AddonsToCreate)+len(plan.AddonsToDelete) != 0 {
		t.Fatalf("export did not round-trip to a no-op addon plan: %+v", plan)
	}
	if len(plan.CronsToCreate)+len(plan.CronsToDelete) != 0 {
		t.Fatalf("export did not round-trip to a no-op cron plan: %+v", plan)
	}
}

func TestExport_OmitsNonAddonSecretRefs(t *testing.T) {
	name := "shop-api"
	svc := typedPlanSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: "shop",
			Runtime: "dockerfile",
			EnvVars: []kube.KusoEnvVar{
				{Name: "API_KEY", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": "manual-secret", "key": "API_KEY"},
				}},
				{Name: "PLAIN", Value: "ok"},
			},
		},
	})
	k, ns := fakeKube(t, seedProject("shop"), svc)
	f, err := Export(context.Background(), k, ns, "shop")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(f.Services) != 1 {
		t.Fatalf("service missing: %+v", f.Services)
	}
	env := f.Services[0].Env
	if _, ok := env["API_KEY"]; ok {
		t.Fatalf("non-addon secret ref must be omitted, got %q", env["API_KEY"])
	}
	if env["PLAIN"] != "ok" {
		t.Fatalf("plain env dropped: %+v", env)
	}
}
