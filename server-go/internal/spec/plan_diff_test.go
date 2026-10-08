package spec

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// seedRichService seeds a KusoService CR that sets every field apply
// patches, plus the CR-only fields the YAML can't express (volume
// storageClass/accessMode, static builder/runtime images, buildpacks
// lifecycle image) that apply must preserve rather than reset.
func seedRichService(project, service string) planSeed {
	name := project + "-" + service
	allowEsc := false
	return typedPlanSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project:           project,
			Runtime:           "static",
			Port:              8080,
			Internal:          true,
			PrivateEgress:     true,
			PlatformAPIEgress: true,
			Command:           []string{"./serve", "--port", "8080"},
			Domains:           []kube.KusoDomain{{Host: "api.shop.example.com", TLS: true}, {Host: "*.shop.example.com", TLS: true, TLSSecret: "wild"}},
			Scale: func() *kube.KusoScaleSpec {
				upWindow := 0
				s := &kube.KusoScaleSpec{Max: 6, TargetCPU: 65, ScaleUpStabilizationSeconds: &upWindow, ScaleUpPods: 4}
				s.SetMin(2)
				return s
			}(),
			Sleep:           &kube.KusoServiceSleep{Enabled: true, AfterMinutes: 20, NonProduction: "off", WakeOn: &kube.KusoServiceWake{ExcludePaths: []string{"/hook"}}},
			Placement:       &kube.KusoPlacement{Labels: map[string]string{"region": "eu"}, Nodes: []string{"n1"}},
			Volumes:         []kube.KusoVolume{{Name: "data", MountPath: "/data", SizeGi: 5, StorageClass: "longhorn", AccessMode: "ReadWriteMany"}},
			Static:          &kube.KusoStaticSpec{BuilderImage: "node:22", RuntimeImage: "nginx:1", BuildCmd: "npm run build", OutputDir: "dist"},
			Buildpacks:      &kube.KusoBuildpacksSpec{BuilderImage: "paketo/builder", LifecycleImage: "lifecycle:0.20"},
			Image:           &kube.KusoImage{Repository: "ghcr.io/me/api", Tag: "2.0"},
			Release:         &kube.KusoReleaseSpec{Command: []string{"node", "migrate.js"}, TimeoutSeconds: 600},
			BuildArgs:       map[string]string{"VERSION": "1"},
			PublicEnv:       []string{"NEXT_PUBLIC_API"},
			SecurityContext: &kube.KusoSecurityContext{Capabilities: &kube.KusoCapabilities{Add: []string{"SETUID"}}, AllowPrivilegeEscalation: &allowEsc},
			EnvVars: []kube.KusoEnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": project + "-db-conn", "key": "DATABASE_URL"},
				}},
			},
		},
	})
}

// seedBareService seeds a KusoService with every optional block nil —
// the shape that exposed apply turning absent into `{}`.
func seedBareService(project, service string) planSeed {
	name := project + "-" + service
	return typedPlanSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Runtime: "dockerfile",
			Port:    3000,
			Scale:   func() *kube.KusoScaleSpec { s := &kube.KusoScaleSpec{Max: 5, TargetCPU: 70}; s.SetMin(1); return s }(),
			Sleep:   &kube.KusoServiceSleep{AfterMinutes: 30},
		},
	})
}

func exportAndPlan(t *testing.T, k *kube.Client, ns, project string) (*File, *Plan) {
	t.Helper()
	f, err := Export(context.Background(), k, ns, project)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	return f, plan
}

func assertNoOpPlan(t *testing.T, plan *Plan) {
	t.Helper()
	if n := len(plan.ServicesToCreate) + len(plan.ServicesToUpdate) + len(plan.ServicesToDelete) +
		len(plan.AddonsToCreate) + len(plan.AddonsToUpdate) + len(plan.AddonsToDelete) +
		len(plan.CronsToCreate) + len(plan.CronsToUpdate) + len(plan.CronsToDelete); n != 0 {
		t.Fatalf("export round-trip must plan zero changes, got %+v (changes %+v)", plan, plan.Changes)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("export round-trip must report no field changes, got %+v", plan.Changes)
	}
}

// #38: the unmodified export of a live project must re-plan to zero
// updates — every resource lands in *Unchanged.
func TestPlanFor_ExportRoundTripIsZeroUpdates(t *testing.T) {
	k, ns := fakeKube(t,
		seedProject("shop"),
		seedRichService("shop", "api"),
		seedBareService("shop", "worker"),
		seedFullAddon("shop", "db"),
		seedPlanAddon("shop", "db2"),
		seedFullCron("shop", "nightly"),
	)
	_, plan := exportAndPlan(t, k, ns, "shop")
	assertNoOpPlan(t, plan)
	if !reflect.DeepEqual(plan.ServicesUnchanged, []string{"api", "worker"}) {
		t.Fatalf("servicesUnchanged = %v", plan.ServicesUnchanged)
	}
	if !reflect.DeepEqual(plan.AddonsUnchanged, []string{"db", "db2"}) {
		t.Fatalf("addonsUnchanged = %v", plan.AddonsUnchanged)
	}
	if !reflect.DeepEqual(plan.CronsUnchanged, []string{"nightly"}) {
		t.Fatalf("cronsUnchanged = %v", plan.CronsUnchanged)
	}
}

func changeFor(plan *Plan, resource string) *ResourceChange {
	for i := range plan.Changes {
		if plan.Changes[i].Resource == resource {
			return &plan.Changes[i]
		}
	}
	return nil
}

func fieldFor(rc *ResourceChange, field string) *FieldChange {
	if rc == nil {
		return nil
	}
	for i := range rc.Fields {
		if rc.Fields[i].Field == field {
			return &rc.Fields[i]
		}
	}
	return nil
}

// #38: dropping blocks from the export must surface as field diffs,
// with the data-losing ones flagged destructive.
func TestPlanFor_ReportsFieldDiffsAndFlagsDestructive(t *testing.T) {
	k, ns := fakeKube(t, seedProject("shop"), seedRichService("shop", "api"))
	f, _ := exportAndPlan(t, k, ns, "shop")
	s := &f.Services[0]
	s.Domains = s.Domains[:1] // drop the wildcard
	s.Volumes = nil
	s.Release = nil
	s.Port = 9090
	s.Env["LOG_LEVEL"] = EnvValue{Value: "debug"}

	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if !reflect.DeepEqual(plan.ServicesToUpdate, []string{"api"}) || len(plan.ServicesUnchanged) != 0 {
		t.Fatalf("api must be the one update: update %v unchanged %v", plan.ServicesToUpdate, plan.ServicesUnchanged)
	}
	rc := changeFor(plan, "service:api")
	if rc == nil {
		t.Fatalf("no change entry for service:api: %+v", plan.Changes)
	}
	for field, destructive := range map[string]bool{
		"port": false, "domains": true, "volumes": true, "release": true, "env.LOG_LEVEL": false,
	} {
		fc := fieldFor(rc, field)
		if fc == nil {
			t.Fatalf("missing %s diff in %+v", field, rc.Fields)
		}
		if fc.Destructive != destructive {
			t.Errorf("%s destructive = %v, want %v (%+v)", field, fc.Destructive, destructive, fc)
		}
	}
	if fc := fieldFor(rc, "port"); fc.From != "8080" || fc.To != "9090" {
		t.Errorf("port diff = %+v", fc)
	}
	if fc := fieldFor(rc, "volumes"); !strings.Contains(fc.From, "data") || fc.To != "(none)" {
		t.Errorf("volumes diff = %+v", fc)
	}
	// Env values are never echoed — the dry-run is editor-visible.
	if fc := fieldFor(rc, "env.LOG_LEVEL"); strings.Contains(fc.From+fc.To, "info") || strings.Contains(fc.From+fc.To, "debug") {
		t.Errorf("env diff leaks values: %+v", fc)
	}
	// Unchanged fields stay out of the diff.
	for _, f := range []string{"static", "buildpacks", "placement", "scale.min", "sleep.enabled", "image", "command"} {
		if fieldFor(rc, f) != nil {
			t.Errorf("unchanged field %s reported: %+v", f, fieldFor(rc, f))
		}
	}
}

// The minimal patch apply sends for an unchanged service is empty, for
// EVERY field servicePatchReq sets. Reflection-driven, so a field added
// to servicePatchReq without a diff entry fails here instead of being
// silently re-sent (or silently hidden from the dry-run).
func TestServiceDiff_UnchangedServiceSendsNothing(t *testing.T) {
	for _, seed := range []planSeed{seedRichService("shop", "api"), seedBareService("shop", "api")} {
		live := decodeService(t, seed)
		desired := exportService("shop", *live)
		req, fields := diffServiceSpec(live, desired)
		if len(fields) != 0 {
			t.Fatalf("unchanged service reported diffs: %+v", fields)
		}
		if nonNil := nonNilFields(req); len(nonNil) != 0 {
			t.Fatalf("unchanged service would still send %v", nonNil)
		}
	}
	// And the reflection check is live: every field servicePatchReq
	// sets is one the diff can drop.
	full := servicePatchReq(ServiceSpec{})
	if len(nonNilFields(full)) < 17 {
		t.Fatalf("servicePatchReq sets fewer fields than expected: %v", nonNilFields(full))
	}
}

// An omitted scale: block resets to the create defaults, never to
// min=0 (scale-to-zero).
func TestServicePatchReq_OmittedScaleIsCreateDefault(t *testing.T) {
	req := servicePatchReq(ServiceSpec{Name: "api"})
	if *req.Scale.Min != 1 || *req.Scale.Max != 5 || *req.Scale.TargetCPU != 70 {
		t.Fatalf("omitted scale = %d/%d/%d, want 1/5/70", *req.Scale.Min, *req.Scale.Max, *req.Scale.TargetCPU)
	}
}

// Scale-speed keys diff like the rest of scale, and dropping one from
// the file puts it back on the chart default rather than leaving the
// live override in place.
func TestServiceDiff_ScaleSpeed(t *testing.T) {
	live := decodeService(t, seedRichService("shop", "api"))
	d := exportService("shop", *live)
	if d.Scale.ScaleUpStabilizationSeconds == nil || *d.Scale.ScaleUpStabilizationSeconds != 0 || d.Scale.ScaleUpPods != 4 {
		t.Fatalf("scale speed not exported: %+v", d.Scale)
	}
	d.Scale.ScaleUpStabilizationSeconds = nil
	d.Scale.ScaleUpPods = 0
	d.Scale.ScaleUpPercent = 50
	req, fields := diffServiceSpec(live, d)
	if req.Scale == nil || *req.Scale.ScaleUpStabilizationSeconds != -1 || *req.Scale.ScaleUpPods != 0 || *req.Scale.ScaleUpPercent != 50 {
		t.Fatalf("scale patch = %+v", req.Scale)
	}
	want := map[string][2]string{
		"scale.scaleUpStabilizationSeconds": {"0", "default"},
		"scale.scaleUpPods":                 {"4", "default"},
		"scale.scaleUpPercent":              {"default", "50"},
	}
	if len(fields) != len(want) {
		t.Fatalf("fields = %+v", fields)
	}
	for _, f := range fields {
		if w, ok := want[f.Field]; !ok || f.From != w[0] || f.To != w[1] {
			t.Errorf("unexpected diff %+v", f)
		}
	}
}

// Absent static/buildpacks/placement must not be written as `{}`, and
// CR-only sub-fields the YAML can't express must be carried through.
func TestServiceDiff_PatchOnlyCarriesChangedFieldsAndPreservesCROnly(t *testing.T) {
	live := decodeService(t, seedRichService("shop", "api"))
	desired := exportService("shop", *live)
	desired.Port = 9090
	desired.Static.BuildCmd = "pnpm build"
	desired.Volumes[0].SizeGi = 10
	req, _ := diffServiceSpec(live, desired)
	got := nonNilFields(req)
	if !reflect.DeepEqual(got, []string{"Port", "Volumes", "Static"}) {
		t.Fatalf("patch fields = %v, want [Port Volumes Static]", got)
	}
	if req.Static.BuilderImage != "node:22" || req.Static.RuntimeImage != "nginx:1" {
		t.Fatalf("static CR-only images dropped: %+v", req.Static)
	}
	v := (*req.Volumes)[0]
	if v.StorageClass != "longhorn" || v.AccessMode != "ReadWriteMany" || v.SizeGi != 10 {
		t.Fatalf("volume CR-only fields dropped: %+v", v)
	}

	bare := decodeService(t, seedBareService("shop", "w"))
	d := exportService("shop", *bare)
	d.Port = 1
	req, _ = diffServiceSpec(bare, d)
	if req.Static != nil || req.Buildpacks != nil || req.Placement != nil || req.Image != nil {
		t.Fatalf("absent blocks must not be written: %+v", req)
	}
}

// An omitted placement means "project default" (Clear), not an
// explicit empty override.
func TestServiceDiff_OmittedPlacementClearsOverride(t *testing.T) {
	live := decodeService(t, seedRichService("shop", "api"))
	d := exportService("shop", *live)
	d.Placement = nil
	req, fields := diffServiceSpec(live, d)
	if req.Placement == nil || !req.Placement.Clear {
		t.Fatalf("omitted placement must Clear: %+v", req.Placement)
	}
	if len(fields) != 1 || fields[0].Field != "placement" {
		t.Fatalf("want one placement diff, got %+v", fields)
	}
}

// Apply driven by a PlanFor plan: unchanged services get no writes at
// all; a changed one gets only its changed fields.
func TestApply_UsesPlannedMinimalPatch(t *testing.T) {
	k, ns := fakeKube(t, seedProject("shop"), seedRichService("shop", "api"), seedBareService("shop", "worker"))
	f, _ := exportAndPlan(t, k, ns, "shop")
	for i := range f.Services {
		if f.Services[i].Name == "worker" {
			f.Services[i].Port = 4000
		}
	}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	fp := &fakeProjects{}
	r := &Reconciler{Projects: fp, Addons: &fakeAddons{}, Crons: &fakeCrons{}}
	res, err := r.Apply(context.Background(), plan, f, ApplyOpts{})
	if err != nil || len(res.Errors) != 0 {
		t.Fatalf("Apply: %v %+v", err, res)
	}
	if len(fp.patched) != 1 || fp.patched[0].service != "worker" {
		t.Fatalf("only worker must be patched: %+v", fp.patched)
	}
	if got := nonNilFields(fp.patched[0].req); !reflect.DeepEqual(got, []string{"Port"}) {
		t.Fatalf("worker patch fields = %v, want [Port]", got)
	}
	if len(fp.envSet) != 0 {
		t.Fatalf("unchanged env must not be rewritten: %+v", fp.envSet)
	}
}

// A new {generate} key missing from the Secret is a change even when
// nothing else differs, and a service-ref env var that resolves to the
// literal already stored is not.
func TestPlanFor_EnvRefsAndGenerate(t *testing.T) {
	api := seedRichService("shop", "api")
	web := typedPlanSeed(kube.GVRServices, "KusoService", "shop-web", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-web", Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{Project: "shop", Runtime: "dockerfile", Port: 3000,
			EnvVars: []kube.KusoEnvVar{{Name: "API_URL", Value: "http://shop-api-production.kuso.svc.cluster.local"}}},
	})
	k, ns := fakeKubeWithSecrets(t, nil, seedProject("shop"), api, web)
	f, err := Export(context.Background(), k, ns, "shop")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	for i := range f.Services {
		if f.Services[i].Name == "web" {
			f.Services[i].Env["API_URL"] = EnvValue{Value: "${{ api.URL }}"}
		}
	}
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	assertNoOpPlan(t, plan)

	for i := range f.Services {
		if f.Services[i].Name == "web" {
			f.Services[i].Env["TOKEN"] = EnvValue{Generate: "hex32"}
		}
	}
	plan, err = PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if !reflect.DeepEqual(plan.ServicesToUpdate, []string{"web"}) {
		t.Fatalf("missing generated key must update web: %+v", plan)
	}
	if fc := fieldFor(changeFor(plan, "service:web"), "env.TOKEN"); fc == nil || fc.To != "{generate: hex32}" {
		t.Fatalf("env.TOKEN diff = %+v", fc)
	}
}

// Apply never modifies an existing addon, so addon drift is reported
// but never counted as an update.
func TestPlanFor_AddonDriftIsReportedNotApplied(t *testing.T) {
	k, ns := fakeKube(t, seedProject("shop"), seedFullAddon("shop", "db"))
	f, _ := exportAndPlan(t, k, ns, "shop")
	f.Addons[0].Version = "17"
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if len(plan.AddonsToUpdate) != 0 {
		t.Fatalf("apply has no addon update path; AddonsToUpdate must stay empty: %v", plan.AddonsToUpdate)
	}
	rc := changeFor(plan, "addon:db")
	if rc == nil || !rc.NotApplied || fieldFor(rc, "version") == nil {
		t.Fatalf("addon drift not reported: %+v", plan.Changes)
	}
}

func TestPlanFor_CronDiff(t *testing.T) {
	k, ns := fakeKube(t, seedProject("shop"), seedFullCron("shop", "nightly"))
	f, _ := exportAndPlan(t, k, ns, "shop")
	f.Crons[0].URL = "https://y"
	plan, err := PlanFor(context.Background(), k, ns, f)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if !reflect.DeepEqual(plan.CronsToUpdate, []string{"nightly"}) {
		t.Fatalf("url change must update the cron: %+v", plan)
	}
	if fc := fieldFor(changeFor(plan, "cron:nightly"), "url"); fc == nil || fc.To != "https://y" {
		t.Fatalf("cron url diff = %+v", plan.Changes)
	}
	if req := cronUpdateReq(f.Crons[0]); req.URL == nil || *req.URL != "https://y" {
		t.Fatalf("cron update must carry the url it reports: %+v", req)
	}
}

// --- helpers ---

func decodeService(t *testing.T, seed planSeed) *kube.KusoService {
	t.Helper()
	var svc kube.KusoService
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(seed.obj.Object, &svc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return &svc
}

func nonNilFields(req projects.PatchServiceRequest) []string {
	var out []string
	v := reflect.ValueOf(req)
	for i := 0; i < v.NumField(); i++ {
		if !v.Field(i).IsNil() {
			out = append(out, v.Type().Field(i).Name)
		}
	}
	return out
}

// fakeKubeWithSecrets is fakeKube plus a typed clientset carrying the
// given Secrets, so the managed-secret lookups in Export/PlanFor work.
func fakeKubeWithSecrets(t *testing.T, secs []*corev1.Secret, seeds ...planSeed) (*kube.Client, string) {
	t.Helper()
	k, ns := fakeKube(t, seeds...)
	cs := k8sfake.NewSimpleClientset()
	for _, s := range secs {
		if _, err := cs.CoreV1().Secrets(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed secret: %v", err)
		}
	}
	k.Clientset = cs
	return k, ns
}
