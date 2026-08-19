// Package backuphealth inspects the control-plane Postgres backup
// CronJob (deploy/postgres-backup.yaml) and reports whether the kuso DB
// is actually being backed up off-cluster.
//
// The backup is opt-in: without the operator-supplied
// kuso-postgres-backup Secret the CronJob suspends itself silently, and
// a failing backup is invisible until restore time — when it's too
// late. This package turns that blind spot into (a) a status the
// settings UI renders as a banner and (b) a Watcher that fires a
// one-shot notify event on the healthy↔unhealthy edge so an operator
// who never opens the page still finds out.
//
// It also enumerates other silent-data-loss surfaces so the banner
// can't stay green while data is unprotected: per-addon backup CronJobs
// (ComputeAddons) and per-service app-data PVCs (ComputeServiceVolumes).
// Service volumes (spec.volumes → RWO PVCs rendered by the
// kusoenvironment chart) have NO scheduled-backup mechanism in any kuso
// chart today — ComputeServiceVolumes reports them as uncovered
// (covered=false, healthy=true) so the gap is VISIBLE. A backup
// mechanism is deliberately deferred: a VolumeSnapshot path needs a CSI
// snapshot class that may not exist on k3s+local-path, and an RWO
// tar-to-S3 Job can't co-attach a PVC the app pod already holds — either
// would be a half-working path that fails on some clusters, which is
// worse than an honest "not covered" row. Visibility is the important
// half of the fix and ships whole here.
package backuphealth

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
	"kuso/server/internal/serverstate"
)

const (
	secretName  = "kuso-postgres-backup"
	cronJobName = "kuso-postgres-backup"
	jobLabel    = "app.kubernetes.io/name=kuso-postgres-backup"
	// StaleAfter: the CronJob runs hourly; if the newest success is
	// older than this we flag stale (tolerates one transient
	// failure+retry before crying wolf).
	StaleAfter = 3 * time.Hour
)

// Status is the verdict the UI banner + watcher consume.
type Status struct {
	Configured     bool   `json:"configured"`
	CronJobPresent bool   `json:"cronJobPresent"`
	Suspended      bool   `json:"suspended"`
	Schedule       string `json:"schedule,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastFailureAt  string `json:"lastFailureAt,omitempty"`
	Stale          bool   `json:"stale"`
	Healthy        bool   `json:"healthy"`
	Detail         string `json:"detail"`
	// Indeterminate: a transient (non-NotFound) kube read failed while
	// computing this status — Healthy may be wrong in either
	// direction. The watcher carries its previous verdict across
	// indeterminate ticks instead of flapping; the UI can badge it.
	Indeterminate bool `json:"indeterminate,omitempty"`
}

// Compute reads the Secret + CronJob + recent Jobs and derives the
// verdict. Three cheap kube reads; safe to call on a UI request or a
// watcher tick.
func Compute(ctx context.Context, kc *kube.Client, namespace string) Status {
	var s Status
	if kc == nil {
		s.Detail = detail(s)
		return s
	}

	if _, err := kc.Clientset.CoreV1().Secrets(namespace).
		Get(ctx, secretName, metav1.GetOptions{}); err == nil {
		s.Configured = true
	} else if !apierrors.IsNotFound(err) {
		s.Indeterminate = true
	}

	if cj, err := kc.Clientset.BatchV1().CronJobs(namespace).
		Get(ctx, cronJobName, metav1.GetOptions{}); err == nil {
		s.CronJobPresent = true
		s.Schedule = cj.Spec.Schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			s.Suspended = true
		}
	} else if !apierrors.IsNotFound(err) {
		s.Indeterminate = true
	}

	if jobs, err := kc.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobLabel,
	}); err == nil {
		success, failure := newestTerminalTimes(jobs.Items)
		if !success.IsZero() {
			s.LastSuccessAt = success.UTC().Format(time.RFC3339)
		}
		if !failure.IsZero() {
			s.LastFailureAt = failure.UTC().Format(time.RFC3339)
		}
		s.Stale = success.IsZero() || time.Since(success) > StaleAfter
	} else {
		s.Stale = true // fail-safe: can't read → don't claim healthy
		s.Indeterminate = true
	}

	s.Healthy = s.Configured && s.CronJobPresent && !s.Suspended && !s.Stale
	s.Detail = detail(s)
	return s
}

func newestTerminalTimes(jobs []batchv1.Job) (success, failure time.Time) {
	for i := range jobs {
		j := &jobs[i]
		t := terminalTime(j)
		if t.IsZero() {
			continue
		}
		switch {
		case j.Status.Succeeded > 0:
			if t.After(success) {
				success = t
			}
		case j.Status.Failed > 0:
			if t.After(failure) {
				failure = t
			}
		}
	}
	return success, failure
}

func terminalTime(j *batchv1.Job) time.Time {
	if j.Status.CompletionTime != nil {
		return j.Status.CompletionTime.Time
	}
	conds := append([]batchv1.JobCondition(nil), j.Status.Conditions...)
	sort.Slice(conds, func(a, b int) bool {
		return conds[a].LastTransitionTime.After(conds[b].LastTransitionTime.Time)
	})
	for _, c := range conds {
		if c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed {
			return c.LastTransitionTime.Time
		}
	}
	if j.Status.StartTime != nil {
		return j.Status.StartTime.Time
	}
	return time.Time{}
}

// AddonBackupStatus is the per-addon verdict for chart-rendered addon
// backup CronJobs (<addon>-backup in the project's execution
// namespace). These were previously invisible to every kuso surface:
// `backup health` covered only the control-plane CronJob, so an addon
// with spec.backup whose runs all died on the missing kuso-backup-s3
// Secret (sportnopz, 2026-08-12: 2+ days of failures) reported nothing
// anywhere.
type AddonBackupStatus struct {
	Addon     string `json:"addon"`
	Project   string `json:"project,omitempty"`
	Namespace string `json:"namespace"`
	// Kind is the addon's datastore kind (postgres/redis/mysql/…).
	Kind     string `json:"kind,omitempty"`
	Schedule string `json:"schedule"`
	// ScheduleConfigured: spec.backup.schedule is set on the addon CR.
	// False rows exist so "this addon has NO scheduled backups" is
	// visible — previously unscheduled addons were silently omitted
	// and the health surface implied full coverage.
	ScheduleConfigured bool `json:"scheduleConfigured"`
	// Covered: the chart renders a scheduled backup CronJob for this
	// addon (schedule set AND the addon's kind/shape is supported).
	// False = this addon is NOT covered by kuso's scheduled backups;
	// Detail says why. Coverage gaps are surfaced, not paged on —
	// Healthy stays true for them so alerting behavior is unchanged.
	Covered        bool `json:"covered"`
	CronJobPresent bool `json:"cronJobPresent"`
	// SecretPresent: the kuso-backup-s3 Secret exists in the CronJob's
	// namespace. False = every run fails CreateContainerConfigError.
	SecretPresent  bool   `json:"secretPresent"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastScheduleAt string `json:"lastScheduleAt,omitempty"`
	Healthy        bool   `json:"healthy"`
	Detail         string `json:"detail,omitempty"`
}

// addonBackupLateGrace: a scheduled run that hasn't succeeded within
// this window after its schedule time counts as failing. Generous (6h)
// because LastScheduleTime stamps at job CREATION: a long-running dump
// plus one Forbid-policy retry cycle must not page — a config-level
// failure (missing Secret) is caught separately and immediately.
const addonBackupLateGrace = 6 * time.Hour

// ComputeAddons lists ALL addon CRs and derives a per-addon backup
// verdict: for scheduled+rendered addons from the CronJob's status +
// the backup Secret's presence, and for everything else an honest
// "not covered by scheduled backups" row (Covered=false, Detail says
// why) — unscheduled addons, kinds with no dump path (clickhouse,
// meilisearch, rabbitmq, …), HA postgres (CNPG owns backups),
// external and instance-shared addons. Coverage gaps are Healthy=true
// so they surface without paging. Addon CRs live in their project's EXECUTION
// namespace (custom-namespace projects — the koreni pattern), so the
// listing fans out over home + every project spec.namespace; a
// home-only list silently missed exactly the projects the check
// exists for.
//
// The second return is false when the evaluation was INCOMPLETE — a
// list failed or a transient (non-NotFound) GET errored. Callers that
// ALERT on the result must not treat an incomplete evaluation as
// either healthy (spurious recovery) or unhealthy (false page); the
// Watcher carries its previous verdict across incomplete ticks.
func ComputeAddons(ctx context.Context, kc *kube.Client, namespace string) ([]AddonBackupStatus, bool) {
	if kc == nil || kc.Clientset == nil {
		return nil, true
	}
	complete := true
	execNS := map[string]string{}
	namespaces := []string{namespace}
	seenNS := map[string]bool{namespace: true}
	if projects, perr := kc.ListKusoProjects(ctx, namespace); perr == nil {
		for i := range projects {
			pns := projects[i].Spec.Namespace
			if pns == "" {
				continue
			}
			execNS[projects[i].Name] = pns
			if !seenNS[pns] {
				seenNS[pns] = true
				namespaces = append(namespaces, pns)
			}
		}
	} else {
		complete = false
	}

	var addons []kube.KusoAddon
	for _, ns := range namespaces {
		list, err := kc.ListKusoAddons(ctx, ns)
		if err != nil {
			complete = false
			continue
		}
		addons = append(addons, list...)
	}

	secretPresent := map[string]*bool{} // nil entry = lookup errored
	var out []AddonBackupStatus
	for i := range addons {
		a := &addons[i]
		project := a.Labels["kuso.sislelabs.com/project"]
		ns := execNS[project]
		if ns == "" {
			ns = a.Namespace
		}
		st := AddonBackupStatus{
			Addon:     a.Name,
			Project:   project,
			Namespace: ns,
			Kind:      a.Spec.Kind,
		}
		if a.Spec.Backup == nil || a.Spec.Backup.Schedule == "" {
			// No schedule → not covered by scheduled backups. This row
			// used to be silently omitted, which made the health
			// surface imply coverage it didn't have. Healthy=true: a
			// coverage gap is a fact to surface, not an alert to page
			// on (paging every cluster that simply hasn't opted in
			// would train operators to ignore the real failures).
			st.Healthy = true
			st.Detail = uncoveredDetail(a)
			out = append(out, st)
			continue
		}
		st.Schedule = a.Spec.Backup.Schedule
		st.ScheduleConfigured = true
		if !kube.AddonBackupCronJobRendered(a) {
			// The chart deliberately renders no CronJob for this
			// shape — nothing to monitor, and paging would be a false
			// alarm. The HA-postgres case is still worth surfacing
			// (kuso doesn't plumb CNPG barman itself), but as detail
			// on a healthy row, not a page.
			st.Healthy = true
			if kube.AddonBackupSuppressedHA(a) {
				st.Detail = "schedule set but kuso renders no CronJob in HA mode (CNPG owns backups) — verify CNPG barman backups are configured, or this addon has none"
			} else {
				st.Detail = "no backup CronJob for this addon shape (instance-shared or unsupported kind) — schedule is inert"
			}
			out = append(out, st)
			continue
		}
		st.Covered = true
		if p, seen := secretPresent[ns]; seen {
			if p == nil {
				complete = false
				continue // lookup errored earlier — skip, don't guess
			}
			st.SecretPresent = *p
		} else {
			_, gerr := kc.Clientset.CoreV1().Secrets(ns).Get(ctx, "kuso-backup-s3", metav1.GetOptions{})
			switch {
			case gerr == nil:
				v := true
				secretPresent[ns] = &v
				st.SecretPresent = true
			case apierrors.IsNotFound(gerr):
				v := false
				secretPresent[ns] = &v
				st.SecretPresent = false
			default:
				// Transient apiserver failure ≠ "Secret missing". A
				// false "backups broken" page teaches operators to
				// ignore the real one.
				secretPresent[ns] = nil
				complete = false
				continue
			}
		}
		cj, cerr := kc.Clientset.BatchV1().CronJobs(ns).Get(ctx, a.Name+"-backup", metav1.GetOptions{})
		switch {
		case cerr == nil:
			st.CronJobPresent = true
			if cj.Status.LastSuccessfulTime != nil {
				st.LastSuccessAt = cj.Status.LastSuccessfulTime.UTC().Format(time.RFC3339)
			}
			if cj.Status.LastScheduleTime != nil {
				st.LastScheduleAt = cj.Status.LastScheduleTime.UTC().Format(time.RFC3339)
			}
			// Failing = a run was scheduled but hasn't succeeded since
			// (grace period absorbs in-flight dumps + one retry).
			// Never-scheduled (new CronJob) is healthy-so-far.
			lateFail := cj.Status.LastScheduleTime != nil &&
				time.Since(cj.Status.LastScheduleTime.Time) > addonBackupLateGrace &&
				(cj.Status.LastSuccessfulTime == nil ||
					cj.Status.LastSuccessfulTime.Time.Before(cj.Status.LastScheduleTime.Time))
			st.Healthy = st.SecretPresent && !lateFail
		case apierrors.IsNotFound(cerr):
			// Eligible but not rendered → operator hasn't reconciled
			// (or is wedged). Unhealthy, distinct detail below.
		default:
			complete = false
			continue // transient — skip, don't guess
		}
		switch {
		case !st.SecretPresent:
			st.Detail = "kuso-backup-s3 Secret missing in " + ns + " — every scheduled run no-ops; configure backup settings or remove the schedule"
		case !st.CronJobPresent:
			st.Detail = "backup CronJob not rendered yet (operator reconcile pending or wedged)"
		case !st.Healthy:
			st.Detail = "last scheduled run has not succeeded"
		}
		out = append(out, st)
	}
	return out, complete
}

// uncoveredDetail explains WHY an addon with no backup schedule is not
// covered by scheduled backups. The classification reuses the chart's
// canonical render matrix (kube.AddonBackupCronJobRendered /
// AddonBackupSuppressedHA) via a probe copy with a synthetic schedule,
// so this can never drift from what the chart actually renders.
func uncoveredDetail(a *kube.KusoAddon) string {
	probe := *a
	b := kube.KusoBackup{Schedule: "@probe"}
	if a.Spec.Backup != nil {
		b = *a.Spec.Backup
		b.Schedule = "@probe"
	}
	probe.Spec.Backup = &b
	external := a.Spec.External != nil &&
		(a.Spec.External.SecretName != "" || len(a.Spec.External.SecretKeys) > 0)
	switch {
	case kube.AddonBackupCronJobRendered(&probe):
		return "no backup schedule — not covered by scheduled backups; set spec.backup.schedule to enable " + a.Spec.Kind + " dumps"
	case kube.AddonBackupSuppressedHA(&probe):
		return "not covered by kuso scheduled backups (HA postgres — CNPG owns backups); verify CNPG barman backups are configured out-of-band, or this addon has none"
	case a.Spec.UseInstanceAddon != "":
		return "instance-shared addon — backups (if any) belong to the instance addon, not this CR"
	case external:
		return "external addon — backups are the external provider's responsibility"
	default:
		return "kind " + a.Spec.Kind + " has no scheduled-backup support — not covered by scheduled backups"
	}
}

// ServiceVolumeBackupStatus is the per-service verdict for services that
// declare spec.volumes — first-class app-data PVCs (kusoenvironment
// pvc.yaml) that, unlike addons, have NO scheduled-backup mechanism in
// any kuso chart today. Before this row existed the backup-health
// surface enumerated ONLY KusoAddon CRs, so a service writing user
// uploads to a PVC that was then lost had zero backup AND zero warning:
// the banner stayed green because it never knew the PVC existed.
//
// These rows mirror the addon "honesty" shape (Covered/ScheduleConfigured/
// Detail) but every row is Covered=false today — there is no mechanism to
// be covered BY. Healthy=true because an un-backed-up app PVC is a fact to
// SURFACE, not an alert to page on: paging every service with a volume
// would train operators to ignore the banner. When/if a volume-backup
// mechanism ships, Covered flips to true for opted-in services and this
// shape already carries the fields to report it.
type ServiceVolumeBackupStatus struct {
	Service   string `json:"service"`
	Project   string `json:"project,omitempty"`
	Namespace string `json:"namespace"`
	// Volumes are the declared spec.volumes names on the service — the
	// PVCs that are unprotected. Listed so the operator knows exactly
	// which disks carry the risk.
	Volumes []string `json:"volumes"`
	// VolumeCount is len(Volumes), surfaced separately so a UI can badge
	// "N unprotected volumes" without walking the slice.
	VolumeCount int `json:"volumeCount"`
	// ScheduleConfigured: a backup schedule is configured for this
	// service's volumes. Always false today (no mechanism, no opt-in
	// field) — present so the wire shape is stable when a mechanism lands.
	ScheduleConfigured bool `json:"scheduleConfigured"`
	// Covered: kuso renders a scheduled backup for these volumes. Always
	// false today. False = these PVCs are NOT covered by scheduled
	// backups; Detail says why.
	Covered bool `json:"covered"`
	// Healthy stays true (a coverage gap is surfaced, not paged on), so
	// this addition does NOT change alerting behavior.
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail,omitempty"`
}

// ComputeServiceVolumes lists ALL KusoService CRs (across home + every
// project execution namespace, the same fan-out ComputeAddons uses so
// custom-namespace projects aren't silently skipped) and emits one row
// per service that declares spec.volumes, reporting that its PVCs are
// NOT covered by scheduled backups (covered=false). Services with no
// volumes are omitted — nothing to protect, nothing to report.
//
// The second return is false when the evaluation was INCOMPLETE (a list
// failed): callers that alert must not treat an incomplete sweep as
// authoritative. Every returned row is Healthy=true, so this never
// pages; it exists purely to make the coverage gap VISIBLE. This closes
// the "green banner while service PVC data is silently unprotected" half
// of the finding even though no backup mechanism ships (see
// docs note in this file's package doc / the finding: a volume-snapshot
// path needs a CSI snapshot class that may not exist on k3s+local-path,
// and an RWO tar-to-S3 sidecar can't co-attach a PVC the app pod holds —
// so the mechanism is deferred, and visibility is the important half).
func ComputeServiceVolumes(ctx context.Context, kc *kube.Client, namespace string) ([]ServiceVolumeBackupStatus, bool) {
	// Only the Dynamic client is needed (KusoService / KusoProject LISTs);
	// unlike ComputeAddons this never reads a core Secret, so we don't gate
	// on Clientset — that let a Dynamic-only client (and the test fake)
	// still enumerate volumes.
	if kc == nil || kc.Dynamic == nil {
		return nil, true
	}
	complete := true
	execNS := map[string]string{}
	namespaces := []string{namespace}
	seenNS := map[string]bool{namespace: true}
	if projects, perr := kc.ListKusoProjects(ctx, namespace); perr == nil {
		for i := range projects {
			pns := projects[i].Spec.Namespace
			if pns == "" {
				continue
			}
			execNS[projects[i].Name] = pns
			if !seenNS[pns] {
				seenNS[pns] = true
				namespaces = append(namespaces, pns)
			}
		}
	} else {
		complete = false
	}

	var services []kube.KusoService
	for _, ns := range namespaces {
		list, err := kc.ListKusoServices(ctx, ns)
		if err != nil {
			complete = false
			continue
		}
		services = append(services, list...)
	}

	var out []ServiceVolumeBackupStatus
	for i := range services {
		s := &services[i]
		if len(s.Spec.Volumes) == 0 {
			continue
		}
		project := s.Spec.Project
		if project == "" {
			project = s.Labels["kuso.sislelabs.com/project"]
		}
		ns := execNS[project]
		if ns == "" {
			ns = s.Namespace
		}
		names := make([]string, 0, len(s.Spec.Volumes))
		for _, v := range s.Spec.Volumes {
			names = append(names, v.Name)
		}
		out = append(out, ServiceVolumeBackupStatus{
			Service:     s.Name,
			Project:     project,
			Namespace:   ns,
			Volumes:     names,
			VolumeCount: len(names),
			// No schedule field on KusoService, no mechanism in any chart.
			ScheduleConfigured: false,
			Covered:            false,
			// A coverage gap is surfaced, not paged on.
			Healthy: true,
			Detail:  serviceVolumesUncoveredDetail(names),
		})
	}
	return out, complete
}

// serviceVolumesUncoveredDetail states the honest coverage gap for a
// service's app-data PVCs: there is no scheduled-backup mechanism for
// service volumes today, so the data on these disks is not backed up by
// kuso and a node/PVC loss loses it.
func serviceVolumesUncoveredDetail(volumes []string) string {
	list := strings.Join(volumes, ", ")
	return "service volume(s) [" + list + "] have no scheduled backup — " +
		"kuso does not back up service PVCs (only addon datastores); this app-data " +
		"disk is NOT protected. Back it up out-of-band, or store durable data in a " +
		"backed-up addon (postgres/s3) instead of a raw volume."
}

// ServiceVolumesHealthy is true when every service-volume row is healthy
// — vacuously true today because every row is Healthy=true by design
// (coverage gaps surface, they don't page). Present so the endpoint's
// aggregate shape mirrors AddonsHealthy and a future mechanism that can
// go UNhealthy has a hook.
func ServiceVolumesHealthy(vols []ServiceVolumeBackupStatus) bool {
	for i := range vols {
		if !vols[i].Healthy {
			return false
		}
	}
	return true
}

// AddonsHealthy is true when every configured addon backup is healthy
// (vacuously true with none configured).
func AddonsHealthy(addons []AddonBackupStatus) bool {
	for i := range addons {
		if !addons[i].Healthy {
			return false
		}
	}
	return true
}

// AddonsWorstSeverity ranks how bad the unhealthy set is: a missing
// backup Secret is a config-level certainty ("you have no backups") →
// error; anything else (late run, unreconciled CronJob) may be
// transient → warn.
func AddonsWorstSeverity(addons []AddonBackupStatus) string {
	worst := "warn"
	for i := range addons {
		if !addons[i].Healthy && !addons[i].SecretPresent {
			worst = "error"
		}
	}
	return worst
}

// Watcher periodically checks backup health and fires a one-shot notify
// event on the healthy↔unhealthy edge — so an operator who never opens
// the settings page still learns their control-plane DB stopped being
// backed up. Edge-triggered (not per-tick) so it doesn't spam. Leader-
// gated by the caller (lives in the kube-singletons block).
// DefaultInterval is the check cadence when Watcher.Interval is unset
// (backup is hourly; 15m is responsive enough without hammering the
// apiserver). Exported so main.go can register backuphealth in the
// serverstate liveness registry at the cadence it beats. main.go leaves
// Interval unset, so this is the effective interval.
const DefaultInterval = 15 * time.Minute

type Watcher struct {
	Kube      *kube.Client
	Notify    *notify.Dispatcher
	Namespace string
	Logger    *slog.Logger
	// Interval between checks. Zero → DefaultInterval (backup is hourly;
	// 15m is responsive enough without hammering the apiserver).
	Interval time.Duration

	// lastState is the comma-joined SET of unhealthy subsystems last
	// emitted ("" = all healthy). Edge-triggering on the SET, not a
	// boolean, matters: with a plain healthy/unhealthy flag, an
	// install whose control-plane backup was never configured is
	// latched unhealthy forever, and an addon-backup failure arriving
	// later would never fire any event — the exact silent failure this
	// watcher exists to catch.
	lastState string
	evaluated bool
	// last*OK carry each subsystem's verdict across INDETERMINATE /
	// incomplete evaluations (transient kube read failures): an
	// unknown state must neither page nor "recover" — without the
	// carry, one apiserver flake fired a spurious unhealthy+recovered
	// event pair.
	lastAddonsOK  bool
	addonsWasEval bool
	lastBackupOK  bool
	backupWasEval bool
	lastGCOK      bool
	gcWasEval     bool
	// lastAddonsPart is the addon component of the state string from
	// the last COMPLETE evaluation, reused verbatim during incomplete
	// ones so alternating complete↔incomplete ticks with the same
	// underlying failure can't flip the state and fire spuriously.
	lastAddonsPart string
}

// carryVerdict resolves a subsystem verdict: the current one when the
// evaluation was determinate, else the carried previous verdict
// (healthy before the first determinate evaluation).
func carryVerdict(current, indeterminate bool, last *bool, wasEval *bool) bool {
	if indeterminate {
		if *wasEval {
			return *last
		}
		return true
	}
	*last, *wasEval = current, true
	return current
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	// Initial delay so a fresh boot doesn't alert before the first
	// backup CronJob has had a chance to run.
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		w.tick(ctx)
		serverstate.LoopHeartbeat(serverstate.LoopBackupHealth)
		t.Reset(interval)
	}
}

func (w *Watcher) tick(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	s := Compute(cctx, w.Kube, w.Namespace)
	gc := RegistryGC(cctx, w.Kube, w.Namespace)
	addons, addonsComplete := ComputeAddons(cctx, w.Kube, w.Namespace)
	cancel()

	// Per-subsystem verdicts, carrying the previous one across
	// indeterminate/incomplete evaluations — a transient apiserver
	// flake must neither page nor "recover".
	backupOK := carryVerdict(s.Healthy, s.Indeterminate, &w.lastBackupOK, &w.backupWasEval)
	gcOK := carryVerdict(gc.Healthy, gc.Indeterminate, &w.lastGCOK, &w.gcWasEval)
	addonsOK := carryVerdict(AddonsHealthy(addons), !addonsComplete, &w.lastAddonsOK, &w.addonsWasEval)

	// Build the unhealthy-subsystem set + a combined detail naming
	// EVERY broken subsystem (a single-detail message hid the addon
	// failure behind the control-plane one). The addon part carries
	// the failing addon NAMES so a same-size swap (A recovers, B
	// breaks) still changes the state and fires.
	var parts, details []string
	if !backupOK {
		parts = append(parts, "backup")
		details = append(details, s.Detail)
	}
	if !gcOK {
		parts = append(parts, "registry-gc")
		details = append(details, gc.Detail)
	}
	if !addonsOK {
		part := "addon-backups"
		if addonsComplete {
			var names []string
			for i := range addons {
				if !addons[i].Healthy {
					names = append(names, addons[i].Addon)
					details = append(details, "addon "+addons[i].Addon+": "+addons[i].Detail)
				}
			}
			part += "(" + strings.Join(names, "+") + ")"
			w.lastAddonsPart = part
		} else {
			// Carried verdict on an incomplete slice: reuse the last
			// COMPLETE part verbatim so an incomplete tick can't flip
			// the state string and fire spuriously.
			if w.lastAddonsPart != "" {
				part = w.lastAddonsPart
			}
			details = append(details, "addon backups unhealthy (evaluation incomplete this tick)")
		}
		parts = append(parts, part)
	}
	severity := alertSeverity(s, gc)
	if !addonsOK && addonsComplete {
		// A missing backup Secret is a config-level "you have no
		// backups" → error outranks whatever the others said; a late
		// or unreconciled run may be transient → keep the higher of
		// the two verdicts.
		if AddonsWorstSeverity(addons) == "error" {
			severity = "error"
		}
	}
	// Severity is part of the edge state: an escalation within an
	// unchanged subsystem set (addon CronJob-late warn → Secret-gone
	// error) must fire, not be swallowed as "same set".
	state := strings.Join(parts, ",")
	if state != "" {
		state += "|" + severity
	}
	detailMsg := strings.Join(details, " · ")
	// Only emit when the SET of broken subsystems changes. evaluated
	// guards the very first tick so we don't double-fire; a cold start
	// that's already unhealthy still alerts (lastState zero value "").
	if w.evaluated && state == w.lastState {
		return
	}
	prevState := w.lastState
	w.evaluated, w.lastState = true, state

	if w.Notify == nil {
		return
	}
	switch {
	case state != "":
		title := "Control-plane backup unhealthy"
		switch {
		case !backupOK:
			// keep default title
		case !gcOK:
			title = "Registry garbage-collection unhealthy"
		default:
			title = "Addon backups failing"
		}
		w.Notify.Emit(notify.Event{
			Type:        notify.EventBackupFailed,
			Timestamp:   time.Now().UTC(),
			Title:       title,
			Body:        detailMsg,
			Description: detailMsg,
			Severity:    severity,
		})
		w.Logger.Warn("backup health: unhealthy", "subsystems", state, "detail", detailMsg, "severity", severity)
	case prevState != "":
		// Recovered (everything healthy again).
		w.Notify.Emit(notify.Event{
			Type:      notify.EventBackupOK,
			Timestamp: time.Now().UTC(),
			Title:     "Backup / registry maintenance recovered",
			Body:      "Control-plane backups, registry GC, and addon backups are healthy again.",
			Severity:  "info",
		})
		w.Logger.Info("backup health: recovered")
	}
}

// RegistryGCStatus reports the health of the weekly registry garbage-
// collection CronJob (deploy/registry.yaml). When the GC stops
// succeeding, the in-cluster registry PVC grows unbounded and builds
// eventually fail with an opaque "no space left on device" — this turns
// that into an early signal. Mirrors the backup status shape.
type RegistryGCStatus struct {
	CronJobPresent bool   `json:"cronJobPresent"`
	Suspended      bool   `json:"suspended"`
	Schedule       string `json:"schedule,omitempty"`
	LastSuccessAt  string `json:"lastSuccessAt,omitempty"`
	LastFailureAt  string `json:"lastFailureAt,omitempty"`
	// Stale: no success within ~9 days (the job is weekly; 9d tolerates
	// one missed run + slack before flagging).
	Stale   bool   `json:"stale"`
	Healthy bool   `json:"healthy"`
	Detail  string `json:"detail"`
	// Indeterminate mirrors Status.Indeterminate: a transient kube
	// read failed; Healthy may be wrong. Watcher carries the previous
	// verdict rather than flapping.
	Indeterminate bool `json:"indeterminate,omitempty"`
}

const (
	registryGCCronJobName = "kuso-registry-gc"
	registryGCJobLabel    = "app.kubernetes.io/name=kuso-registry-gc"
	// registryGCStaleAfter: the GC is weekly, so tolerate one missed
	// run (14d) plus a couple days of slack before flagging — a single
	// skipped Sunday isn't an emergency, two in a row is.
	registryGCStaleAfter = 16 * 24 * time.Hour
)

// RegistryGC computes the registry-GC verdict from the GC CronJob + its
// recent Jobs. Two cheap kube reads.
func RegistryGC(ctx context.Context, kc *kube.Client, namespace string) RegistryGCStatus {
	var s RegistryGCStatus
	if kc == nil {
		s.Detail = registryGCDetail(s)
		return s
	}
	if cj, err := kc.Clientset.BatchV1().CronJobs(namespace).
		Get(ctx, registryGCCronJobName, metav1.GetOptions{}); err == nil {
		s.CronJobPresent = true
		s.Schedule = cj.Spec.Schedule
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			s.Suspended = true
		}
	} else if !apierrors.IsNotFound(err) {
		s.Indeterminate = true
	}
	if jobs, err := kc.Clientset.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: registryGCJobLabel,
	}); err != nil {
		s.Indeterminate = true
	} else {
		success, failure := newestTerminalTimes(jobs.Items)
		if !success.IsZero() {
			s.LastSuccessAt = success.UTC().Format(time.RFC3339)
		}
		if !failure.IsZero() {
			s.LastFailureAt = failure.UTC().Format(time.RFC3339)
		}
		// A GC that has never run yet (fresh install, first Sunday not
		// reached) is NOT stale — only flag once it's had a chance and
		// then lapsed. So: stale iff there's a success that's now old,
		// OR there's a failure but no success.
		switch {
		case !success.IsZero():
			s.Stale = time.Since(success) > registryGCStaleAfter
		case !failure.IsZero():
			s.Stale = true // failing with no success ever
		default:
			s.Stale = false // never run yet — warming up
		}
	}
	s.Healthy = s.CronJobPresent && !s.Suspended && !s.Stale
	s.Detail = registryGCDetail(s)
	return s
}

func registryGCDetail(s RegistryGCStatus) string {
	switch {
	case !s.CronJobPresent:
		return "Registry garbage-collection CronJob not found — the build-image registry will grow unbounded and eventually fill its disk."
	case s.Suspended:
		return "Registry garbage-collection is suspended — reclaimed image space will stop being freed."
	case s.Stale:
		return "Registry garbage-collection hasn't succeeded recently — the registry PVC may be filling. Check the kuso-registry-gc CronJob."
	default:
		return "Registry garbage-collection healthy."
	}
}

// alertSeverity decides the notify severity for an unhealthy verdict,
// which in turn gates the Discord @here mention (error pings, warn
// doesn't — see notify.mentionFor). The split: a subsystem that was
// never configured (no Secret / no CronJob) is a `warn` nudge — an
// operator who hasn't set up off-cluster backups shouldn't get @here-
// paged on every kuso-server restart. A subsystem that WAS configured
// and then suspended or went stale is a real regression → `error`
// (+ @here). Caller only invokes this when something is unhealthy.
func alertSeverity(s Status, gc RegistryGCStatus) string {
	backupConfigured := s.Configured && s.CronJobPresent
	gcConfigured := gc.CronJobPresent
	if !s.Healthy && backupConfigured {
		return "error" // backup was set up and broke
	}
	if s.Healthy && !gc.Healthy && gcConfigured {
		return "error" // GC was set up and broke
	}
	return "warn" // unconfigured / never-set-up — informational
}

func detail(s Status) string {
	switch {
	case !s.CronJobPresent:
		return "Control-plane backup CronJob not found — the kuso DB is not being backed up off-cluster."
	case !s.Configured:
		return "Control-plane backups are not configured. Create the kuso-postgres-backup Secret with S3 credentials so the kuso DB is backed up off-cluster — without it a node/PVC loss orphans every project."
	case s.Suspended:
		return "Control-plane backup CronJob is suspended — no backups are being taken."
	case s.LastSuccessAt == "":
		return "Control-plane backups are configured but none have succeeded yet."
	case s.Stale:
		return "Control-plane backups are configured but the last successful backup is stale — backups may have silently stopped. Check the kuso-postgres-backup CronJob."
	default:
		return "Control-plane backups healthy."
	}
}
