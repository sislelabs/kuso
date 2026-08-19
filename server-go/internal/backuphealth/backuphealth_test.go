package backuphealth

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// TestDetail locks the verdict precedence: missing CronJob → not
// configured → suspended → never succeeded → stale → healthy. The
// banner renders Detail verbatim, so order matters.
func TestDetail(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Status
		want string
	}{
		{"no cronjob", Status{}, "not found"},
		{"not configured", Status{CronJobPresent: true}, "not configured"},
		{"suspended", Status{CronJobPresent: true, Configured: true, Suspended: true}, "suspended"},
		{"never succeeded", Status{CronJobPresent: true, Configured: true}, "none have succeeded"},
		{"stale", Status{CronJobPresent: true, Configured: true, LastSuccessAt: "2020-01-01T00:00:00Z", Stale: true}, "stale"},
		{"healthy", Status{CronJobPresent: true, Configured: true, LastSuccessAt: "2020-01-01T00:00:00Z"}, "healthy"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := detail(tc.in); !strings.Contains(got, tc.want) {
				t.Errorf("detail = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestNewestTerminalTimes(t *testing.T) {
	t.Parallel()
	mk := func(success bool, at time.Time) batchv1.Job {
		j := batchv1.Job{Status: batchv1.JobStatus{CompletionTime: &metav1.Time{Time: at}}}
		if success {
			j.Status.Succeeded = 1
		} else {
			j.Status.Failed = 1
		}
		return j
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jobs := []batchv1.Job{
		mk(true, t0),
		mk(true, t0.Add(2*time.Hour)),  // newest success
		mk(false, t0.Add(1*time.Hour)), // newest failure
		{Status: batchv1.JobStatus{Active: 1}},
	}
	success, failure := newestTerminalTimes(jobs)
	if !success.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("success = %v, want %v", success, t0.Add(2*time.Hour))
	}
	if !failure.Equal(t0.Add(1 * time.Hour)) {
		t.Errorf("failure = %v, want %v", failure, t0.Add(1*time.Hour))
	}
}

// TestWatcherEdgeTriggers verifies the watcher fires only when the SET
// of unhealthy subsystems changes — not just on a healthy/unhealthy
// boolean flip. The set matters: an install whose control-plane backup
// was never configured is "unhealthy" forever, and with boolean edge
// logic an addon-backup failure arriving later would never fire.
func TestWatcherEdgeTriggers(t *testing.T) {
	t.Parallel()
	w := &Watcher{}

	// Simulate the tick's decision logic directly via a small driver,
	// since tick() does kube I/O. Mirrors the lastState bookkeeping.
	type emit struct {
		state     string
		recovered bool
	}
	var emits []emit
	step := func(state string) {
		if w.evaluated && state == w.lastState {
			return
		}
		prev := w.lastState
		w.evaluated, w.lastState = true, state
		switch {
		case state != "":
			emits = append(emits, emit{state: state})
		case prev != "":
			emits = append(emits, emit{recovered: true})
		}
	}

	step("backup")               // first: unhealthy → fire
	step("backup")               // same set → no fire
	step("backup,addon-backups") // set GREW (addon failure on top) → fire
	step("backup,addon-backups") // same → no fire
	step("backup")               // addon recovered, backup still bad → fire
	step("")                     // all recovered → fire recovered
	step("")                     // still healthy → no fire
	step("addon-backups")        // unhealthy again → fire

	if len(emits) != 5 {
		t.Fatalf("expected 5 edge emits, got %d: %+v", len(emits), emits)
	}
	if emits[0].state != "backup" ||
		emits[1].state != "backup,addon-backups" ||
		emits[2].state != "backup" ||
		!emits[3].recovered ||
		emits[4].state != "addon-backups" {
		t.Errorf("edge sequence wrong: %+v", emits)
	}
}

// TestUncoveredDetail locks the honesty classification for addons with
// NO backup schedule — previously these were silently omitted from the
// health surface, so "not covered by scheduled backups" was invisible.
// The classification must agree with the chart's render matrix (probed
// via kube.AddonBackupCronJobRendered on a synthetic schedule).
func TestUncoveredDetail(t *testing.T) {
	t.Parallel()
	mk := func(kind string, mut func(*kube.KusoAddon)) *kube.KusoAddon {
		a := &kube.KusoAddon{}
		a.Spec.Kind = kind
		if mut != nil {
			mut(a)
		}
		return a
	}
	cases := []struct {
		name string
		in   *kube.KusoAddon
		want string
	}{
		// Kinds the chart CAN back up → actionable nudge.
		{"postgres schedulable", mk("postgres", nil), "set spec.backup.schedule"},
		{"redis schedulable", mk("redis", nil), "set spec.backup.schedule"},
		{"mysql schedulable", mk("mysql", nil), "set spec.backup.schedule"},
		{"mongodb schedulable", mk("mongodb", nil), "set spec.backup.schedule"},
		{"s3 schedulable", mk("s3", nil), "set spec.backup.schedule"},
		// External postgres IS schedulable (external-pg CronJob variant).
		{"external postgres schedulable", mk("postgres", func(a *kube.KusoAddon) {
			a.Spec.External = &kube.KusoAddonExternal{SecretName: "byo"}
		}), "set spec.backup.schedule"},
		// HA postgres: chart suppresses pg_dump; CNPG owns backups.
		{"ha postgres", mk("postgres", func(a *kube.KusoAddon) { a.Spec.HA = true }), "CNPG"},
		// Instance-shared: backups belong to the instance addon.
		{"instance-shared", mk("postgres", func(a *kube.KusoAddon) { a.Spec.UseInstanceAddon = "pg-main" }), "instance addon"},
		// External non-postgres: provider's responsibility.
		{"external mysql", mk("mysql", func(a *kube.KusoAddon) {
			a.Spec.External = &kube.KusoAddonExternal{SecretName: "byo"}
		}), "provider's responsibility"},
		// Kinds with no dump path → honest "no support" statement.
		{"clickhouse unsupported", mk("clickhouse", nil), "no scheduled-backup support"},
		{"meilisearch unsupported", mk("meilisearch", nil), "no scheduled-backup support"},
		{"rabbitmq unsupported", mk("rabbitmq", nil), "no scheduled-backup support"},
		{"valkey unsupported", mk("valkey", nil), "no scheduled-backup support"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := uncoveredDetail(tc.in)
			if !strings.Contains(got, tc.want) {
				t.Errorf("uncoveredDetail = %q, want substring %q", got, tc.want)
			}
			if !strings.Contains(got, "not covered") && !strings.Contains(got, "backups") {
				t.Errorf("uncoveredDetail = %q must mention the coverage gap", got)
			}
		})
	}
}

// TestUncoveredDetailDoesNotMutate guards the probe-copy trick: probing
// the chart render matrix with a synthetic schedule must never leak
// into the caller's addon CR.
func TestUncoveredDetailDoesNotMutate(t *testing.T) {
	t.Parallel()
	a := &kube.KusoAddon{}
	a.Spec.Kind = "postgres"
	_ = uncoveredDetail(a)
	if a.Spec.Backup != nil {
		t.Fatalf("uncoveredDetail mutated the input addon: Backup=%+v", a.Spec.Backup)
	}
	b := &kube.KusoAddon{}
	b.Spec.Kind = "redis"
	b.Spec.Backup = &kube.KusoBackup{RetentionDays: 7}
	_ = uncoveredDetail(b)
	if b.Spec.Backup.Schedule != "" {
		t.Fatalf("uncoveredDetail mutated the input schedule: %q", b.Spec.Backup.Schedule)
	}
}

// --- service-volume backup visibility ---------------------------------

// fakeKube builds a *kube.Client backed by dynamic/fake, seeded with the
// given CRs. Mirrors internal/kube's own test fake (which is package-
// private), scoped down to the two GVRs ComputeServiceVolumes lists.
func fakeKube(t *testing.T, objs ...*unstructured.Unstructured) *kube.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, o := range objs {
		gvr := kube.GVRServices
		if o.GetKind() == "KusoProject" {
			gvr = kube.GVRProjects
		}
		if err := dyn.Tracker().Create(gvr, o, o.GetNamespace()); err != nil {
			t.Fatalf("seed %s/%s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	return &kube.Client{Dynamic: dyn}
}

func svcCR(namespace, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   kube.GVRServices.Group,
		Version: kube.GVRServices.Version,
		Kind:    "KusoService",
	})
	u.SetNamespace(namespace)
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, spec, "spec")
	return u
}

func projCR(namespace, name, execNS string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   kube.GVRProjects.Group,
		Version: kube.GVRProjects.Version,
		Kind:    "KusoProject",
	})
	u.SetNamespace(namespace)
	u.SetName(name)
	spec := map[string]any{"project": name}
	if execNS != "" {
		spec["namespace"] = execNS
	}
	_ = unstructured.SetNestedField(u.Object, spec, "spec")
	return u
}

func vol(name, mountPath string) map[string]any {
	return map[string]any{"name": name, "mountPath": mountPath}
}

// TestComputeServiceVolumes verifies that services declaring spec.volumes
// surface as uncovered rows (covered=false, healthy=true) while services
// with no volumes are omitted — the core "green banner while PVC data is
// unprotected" fix. Every row is healthy (a coverage gap is surfaced, not
// paged on), so this never changes alerting behavior.
func TestComputeServiceVolumes(t *testing.T) {
	t.Parallel()
	kc := fakeKube(t,
		svcCR("kuso", "web", map[string]any{
			"project": "alpha",
			"volumes": []any{vol("uploads", "/data/uploads")},
		}),
		svcCR("kuso", "api", map[string]any{
			"project": "alpha",
			// no volumes → omitted
		}),
		svcCR("kuso", "worker", map[string]any{
			"project": "alpha",
			"volumes": []any{vol("cache", "/var/cache"), vol("state", "/var/lib/state")},
		}),
	)
	rows, complete := ComputeServiceVolumes(context.Background(), kc, "kuso")
	if !complete {
		t.Fatalf("expected complete evaluation, got incomplete")
	}
	byName := map[string]ServiceVolumeBackupStatus{}
	for _, r := range rows {
		byName[r.Service] = r
	}
	if _, ok := byName["api"]; ok {
		t.Errorf("service with no volumes must be omitted; got a row for 'api'")
	}
	web, ok := byName["web"]
	if !ok {
		t.Fatalf("expected a row for 'web'")
	}
	if web.Covered || web.ScheduleConfigured {
		t.Errorf("web must be uncovered/unscheduled (no mechanism exists): %+v", web)
	}
	if !web.Healthy {
		t.Errorf("web must be healthy (coverage gap surfaces, not pages): %+v", web)
	}
	if web.Project != "alpha" {
		t.Errorf("web.Project = %q, want alpha", web.Project)
	}
	if len(web.Volumes) != 1 || web.Volumes[0] != "uploads" || web.VolumeCount != 1 {
		t.Errorf("web volumes = %v (count %d), want [uploads]/1", web.Volumes, web.VolumeCount)
	}
	if !strings.Contains(web.Detail, "not protected") && !strings.Contains(web.Detail, "no scheduled backup") {
		t.Errorf("web.Detail must name the coverage gap: %q", web.Detail)
	}
	worker := byName["worker"]
	if worker.VolumeCount != 2 {
		t.Errorf("worker.VolumeCount = %d, want 2", worker.VolumeCount)
	}
	if !ServiceVolumesHealthy(rows) {
		t.Errorf("ServiceVolumesHealthy must be true — every row is healthy by design")
	}
}

// TestComputeServiceVolumesCustomNamespace verifies the fan-out over
// project execution namespaces (the koreni custom-namespace pattern): a
// service living in a project's spec.namespace, not the home namespace,
// must still be enumerated — a home-only list would silently miss exactly
// the projects the check exists for.
func TestComputeServiceVolumesCustomNamespace(t *testing.T) {
	t.Parallel()
	kc := fakeKube(t,
		projCR("kuso", "koreni", "koreni-ns"),
		svcCR("koreni-ns", "uploader", map[string]any{
			"project": "koreni",
			"volumes": []any{vol("blobs", "/data/blobs")},
		}),
	)
	rows, complete := ComputeServiceVolumes(context.Background(), kc, "kuso")
	if !complete {
		t.Fatalf("expected complete evaluation")
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row from the custom namespace, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Service != "uploader" || r.Namespace != "koreni-ns" {
		t.Errorf("row = %+v, want service=uploader ns=koreni-ns", r)
	}
}

// TestComputeServiceVolumesNilClient guards the fail-safe: a nil client
// returns complete=true with no rows (nothing to enumerate) rather than
// panicking or claiming an incomplete sweep.
func TestComputeServiceVolumesNilClient(t *testing.T) {
	t.Parallel()
	rows, complete := ComputeServiceVolumes(context.Background(), nil, "kuso")
	if len(rows) != 0 || !complete {
		t.Errorf("nil client: rows=%v complete=%v, want []/true", rows, complete)
	}
}

// TestServiceVolumesUncoveredDetail locks the honesty message: it must
// name the specific volumes and state plainly that the data is NOT backed
// up by kuso — the whole point of the visibility fix.
func TestServiceVolumesUncoveredDetail(t *testing.T) {
	t.Parallel()
	got := serviceVolumesUncoveredDetail([]string{"uploads", "state"})
	for _, want := range []string{"uploads", "state", "no scheduled backup", "NOT protected"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail = %q, want substring %q", got, want)
		}
	}
}

// TestAlertSeverity pins the warn-vs-error split that controls whether
// the Discord alert @here-pings: a never-configured backup/GC is a warn
// nudge (no ping, no paging on every restart); a configured-then-broken
// one is an error (pings). This is the fix for the "@here on every
// release roll" noise on an unconfigured control-plane backup.
func TestAlertSeverity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		s    Status
		gc   RegistryGCStatus
		want string
	}{
		{
			name: "backup never configured → warn",
			s:    Status{Configured: false, CronJobPresent: false, Healthy: false},
			gc:   RegistryGCStatus{CronJobPresent: true, Healthy: true},
			want: "warn",
		},
		{
			name: "backup CronJob present but no secret → warn",
			s:    Status{Configured: false, CronJobPresent: true, Healthy: false},
			gc:   RegistryGCStatus{CronJobPresent: true, Healthy: true},
			want: "warn",
		},
		{
			name: "backup configured but stale → error",
			s:    Status{Configured: true, CronJobPresent: true, Stale: true, Healthy: false},
			gc:   RegistryGCStatus{CronJobPresent: true, Healthy: true},
			want: "error",
		},
		{
			name: "backup configured but suspended → error",
			s:    Status{Configured: true, CronJobPresent: true, Suspended: true, Healthy: false},
			gc:   RegistryGCStatus{CronJobPresent: true, Healthy: true},
			want: "error",
		},
		{
			name: "GC never configured (backup healthy) → warn",
			s:    Status{Configured: true, CronJobPresent: true, Healthy: true},
			gc:   RegistryGCStatus{CronJobPresent: false, Healthy: false},
			want: "warn",
		},
		{
			name: "GC configured but stale (backup healthy) → error",
			s:    Status{Configured: true, CronJobPresent: true, Healthy: true},
			gc:   RegistryGCStatus{CronJobPresent: true, Stale: true, Healthy: false},
			want: "error",
		},
	}
	for _, tc := range cases {
		if got := alertSeverity(tc.s, tc.gc); got != tc.want {
			t.Errorf("%s: alertSeverity = %q, want %q", tc.name, got, tc.want)
		}
	}
}
