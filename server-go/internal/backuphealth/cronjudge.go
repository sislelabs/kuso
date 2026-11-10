package backuphealth

import (
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
)

// stuckRunAfter flags a backup Job still running this long after it
// started. It matches the late grace: a Forbid-policy CronJob skips every
// run while one is active, so a wedged dump silently costs every night.
const stuckRunAfter = addonBackupLateGrace

type cronVerdict struct {
	lastSuccess time.Time
	problem     string // "" = healthy
}

// isManualRun reports a "backup now" Job (or kubectl create job --from).
func isManualRun(j *batchv1.Job) bool {
	return j.Annotations["cronjob.kubernetes.io/instantiate"] == "manual"
}

func ownedBy(j *batchv1.Job, cj *batchv1.CronJob) bool {
	for _, o := range j.OwnerReferences {
		if o.Kind == "CronJob" && o.Name == cj.Name {
			return true
		}
	}
	return false
}

// judgeCronJob decides an addon backup CronJob's health from its spec,
// status and the Jobs it owns.
//
// The CronJob's own lastSuccessfulTime counts a manual run too, so a "backup
// now" that succeeded would hide every failed scheduled night. Only the
// scheduled Jobs decide; the status field is a fallback once the history
// limit has removed them, and only when no manual run could have set it.
func judgeCronJob(cj *batchv1.CronJob, jobs []batchv1.Job, now time.Time) cronVerdict {
	var scheduled []batchv1.Job
	var manualSuccess time.Time
	var stuck *batchv1.Job
	for i := range jobs {
		j := &jobs[i]
		if !ownedBy(j, cj) {
			continue
		}
		done := j.Status.Succeeded > 0 || j.Status.Failed > 0 || j.Status.CompletionTime != nil
		if !done && j.Status.StartTime != nil && now.Sub(j.Status.StartTime.Time) > stuckRunAfter {
			if stuck == nil || j.Status.StartTime.Before(stuck.Status.StartTime) {
				stuck = j
			}
		}
		if isManualRun(j) {
			if s, _ := newestTerminalTimes([]batchv1.Job{*j}); s.After(manualSuccess) {
				manualSuccess = s
			}
			continue
		}
		scheduled = append(scheduled, *j)
	}
	success, _ := newestTerminalTimes(scheduled)
	if success.IsZero() && cj.Status.LastSuccessfulTime != nil {
		if fb := cj.Status.LastSuccessfulTime.Time; manualSuccess.IsZero() || fb.After(manualSuccess) {
			success = fb
		}
	}

	v := cronVerdict{lastSuccess: success}
	switch {
	case cj.Spec.Suspend != nil && *cj.Spec.Suspend:
		v.problem = "backup CronJob is suspended; no scheduled runs happen"
	case stuck != nil:
		v.problem = fmt.Sprintf("backup run %s has been running for %s; while it runs every scheduled run is skipped",
			stuck.Name, now.Sub(stuck.Status.StartTime.Time).Round(time.Minute))
	case cj.Status.LastScheduleTime != nil &&
		now.Sub(cj.Status.LastScheduleTime.Time) > addonBackupLateGrace &&
		success.Before(cj.Status.LastScheduleTime.Time):
		// A run was scheduled and nothing scheduled has succeeded since
		// (the grace absorbs a long dump plus one retry). A never-scheduled
		// CronJob is healthy so far.
		v.problem = "last scheduled run has not succeeded"
	}
	return v
}
