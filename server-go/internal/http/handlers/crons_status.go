package handlers

import (
	"context"
	"maps"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// withCronJobStatus copies the rendered CronJob's run state onto each
// KusoCron's status: lastScheduleTime, lastSuccessfulTime and active. The
// KusoCron status is the helm-operator's (conditions + deployedRelease
// only), so without this every cron read null for both times even while
// its Jobs ran. Best-effort: on a list error the crons go out unchanged.
// Returns copies; the input may be informer-cache objects.
func withCronJobStatus(ctx context.Context, k *kube.Client, in []kube.KusoCron) []kube.KusoCron {
	if k == nil || k.Clientset == nil || len(in) == 0 {
		return in
	}
	byNS := map[string]map[string]*batchv1.CronJob{}
	out := make([]kube.KusoCron, len(in))
	for i := range in {
		out[i] = in[i]
		ns := in[i].Namespace
		jobs, ok := byNS[ns]
		if !ok {
			jobs = map[string]*batchv1.CronJob{}
			list, err := k.Clientset.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{
				LabelSelector: "app.kubernetes.io/name=kusocron",
			})
			if err == nil {
				for j := range list.Items {
					jobs[list.Items[j].Name] = &list.Items[j]
				}
			}
			byNS[ns] = jobs
		}
		cj := jobs[cronJobName(in[i].Name)]
		if cj == nil {
			continue
		}
		st := make(map[string]any, len(in[i].Status)+3)
		maps.Copy(st, in[i].Status)
		if t := cj.Status.LastScheduleTime; t != nil {
			st["lastScheduleTime"] = t.UTC().Format(time.RFC3339)
		}
		if t := cj.Status.LastSuccessfulTime; t != nil {
			st["lastSuccessfulTime"] = t.UTC().Format(time.RFC3339)
		}
		st["active"] = len(cj.Status.Active)
		out[i].Status = st
	}
	return out
}

// cronJobName mirrors the kusocron chart's fullname: the release (CR)
// name truncated to 63 chars with a trailing "-" trimmed.
func cronJobName(cr string) string {
	if len(cr) > 63 {
		cr = cr[:63]
	}
	return strings.TrimSuffix(cr, "-")
}
