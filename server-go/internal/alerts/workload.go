package alerts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/uptime"
)

// Defaults for the workload kinds when a rule leaves a threshold unset.
const (
	defaultCrashRestarts   = 3
	defaultHealthcheckMins = 5
)

// Build annotations the poller stamps (mirrors builds.AnnBuildPhase /
// AnnBuildCompletedAt; alerts can't import builds without a cycle risk).
const (
	annBuildPhase       = "kuso.sislelabs.com/build-phase"
	annBuildCompletedAt = "kuso.sislelabs.com/build-completed-at"
)

// ---- crashloop ---------------------------------------------------------

func (e *Engine) evalCrashloop(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
	envs, err := e.scopedEnvs(ctx, r)
	if err != nil || len(envs) == 0 {
		return finding{}, err
	}
	pods := map[string][]*corev1.Pod{}
	for _, t := range envs {
		ps, err := e.envPods(ctx, t)
		if err != nil {
			return finding{}, err
		}
		pods[t.name] = ps
	}
	return crashloopFinding(envs, pods, thresholdInt(r, defaultCrashRestarts), ruleWindow(r), now), nil
}

// envPods lists an env's pods, from the informer when it's warm.
func (e *Engine) envPods(ctx context.Context, t envTarget) ([]*corev1.Pod, error) {
	sel := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/instance": t.name})
	if e.Kube.Cache != nil {
		if cached, ok := e.Kube.Cache.ListPodsByLabel(sel); ok {
			out := cached[:0:0]
			for _, p := range cached {
				if p.Namespace == t.namespace {
					out = append(out, p)
				}
			}
			return out, nil
		}
	}
	if e.Kube.Clientset == nil {
		return nil, nil
	}
	list, err := e.Kube.Clientset.CoreV1().Pods(t.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, fmt.Errorf("list pods %s/%s: %w", t.namespace, t.name, err)
	}
	out := make([]*corev1.Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// crashloopFinding flags an env when one of its containers is backing off
// right now, or has restarted at least `restarts` times with the latest
// restart inside the window. Kubernetes only keeps a cumulative restart
// count, so "recent" is judged by when the last run ended.
func crashloopFinding(envs []envTarget, pods map[string][]*corev1.Pod, restarts int64, window time.Duration, now time.Time) finding {
	var f finding
	for _, t := range envs {
		for _, p := range pods[t.name] {
			if p.DeletionTimestamp != nil || p.Labels["job-name"] != "" {
				continue
			}
			for _, c := range p.Status.ContainerStatuses {
				backingOff := c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff"
				recent := false
				if term := c.LastTerminationState.Terminated; term != nil {
					recent = now.Sub(term.FinishedAt.Time) <= window
				}
				if backingOff || (int64(c.RestartCount) >= restarts && recent) {
					f.add(t.name, fmt.Sprintf("%s: container %s restarted %d times%s",
						t.scope(), c.Name, c.RestartCount, crashSuffix(backingOff)))
					break
				}
			}
			if _, hit := f.details[t.name]; hit {
				break
			}
		}
	}
	sort.Strings(f.targets)
	return f
}

func crashSuffix(backingOff bool) string {
	if backingOff {
		return " and is in CrashLoopBackOff"
	}
	return ""
}

// ---- deploy_failed -----------------------------------------------------

func (e *Engine) evalDeployFailed(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
	builds, err := e.Kube.ListKusoBuilds(ctx, "")
	if err != nil {
		return finding{}, fmt.Errorf("list builds: %w", err)
	}
	return deployFailedFinding(r, builds, now), nil
}

// deployFailedFinding flags a service whose newest finished build failed
// (build or release hook). Only failures inside the window open a target,
// so a new rule doesn't page for last month's broken branch; an open
// target stays firing until a later build of that service succeeds.
// Cancelled builds (deleted branch, superseded) are not failures.
func deployFailedFinding(r *db.AlertRule, builds []kube.KusoBuild, now time.Time) finding {
	type latest struct {
		at    time.Time
		phase string
		name  string
	}
	newest := map[string]latest{}
	for i := range builds {
		b := &builds[i]
		phase := b.Annotations[annBuildPhase]
		if phase != "succeeded" && phase != "failed" && phase != "release-failed" {
			continue
		}
		project := b.Spec.Project
		svc := strings.TrimPrefix(b.Spec.Service, project+"-")
		if r.Project != "" && project != r.Project {
			continue
		}
		if want := strings.TrimPrefix(r.Service, r.Project+"-"); want != "" && svc != want {
			continue
		}
		at, err := time.Parse(time.RFC3339, b.Annotations[annBuildCompletedAt])
		if err != nil {
			continue
		}
		key := project + "/" + svc
		if cur, ok := newest[key]; !ok || at.After(cur.at) {
			newest[key] = latest{at: at, phase: phase, name: b.Name}
		}
	}
	firing := map[string]bool{}
	for _, t := range r.FiringTargets {
		firing[t] = true
	}
	window := ruleWindow(r)
	var f finding
	for key, l := range newest {
		if l.phase == "succeeded" {
			continue
		}
		if !firing[key] && now.Sub(l.at) > window {
			continue
		}
		what := "build failed"
		if l.phase == "release-failed" {
			what = "release hook failed; the previous version keeps serving"
		}
		f.add(key, fmt.Sprintf("%s: %s (%s)", key, what, l.name))
	}
	sort.Strings(f.targets)
	return f
}

// ---- healthcheck_failing -----------------------------------------------

func (e *Engine) evalHealthcheck(ctx context.Context, r *db.AlertRule, now time.Time) (finding, error) {
	if e.DB == nil {
		return finding{}, nil
	}
	rows, err := e.DB.ListUptimeStates(ctx, r.Project)
	if err != nil {
		return finding{}, err
	}
	return healthcheckFinding(r, rows, now), nil
}

// healthcheckFinding flags an env whose uptime check has been failing for
// at least ThresholdInt minutes. Paused envs (stopped, sleeping, rolling
// out) and rows whose last check is stale are left alone: a stale row is
// the uptime loop being down, not the service.
func healthcheckFinding(r *db.AlertRule, rows []db.UptimeState, now time.Time) finding {
	minFor := time.Duration(thresholdInt(r, defaultHealthcheckMins)) * time.Minute
	var f finding
	for _, row := range rows {
		if r.Project != "" && row.Project != r.Project {
			continue
		}
		if want := strings.TrimPrefix(r.Service, r.Project+"-"); want != "" && row.Service != want {
			continue
		}
		if r.Env != "" && row.Env != r.Env && !strings.HasSuffix(row.Env, "-"+r.Env) {
			continue
		}
		if row.Paused != "" || row.DownSince.IsZero() || row.LastCheckedAt.IsZero() {
			continue
		}
		if now.Sub(row.LastCheckedAt) > uptime.StaleAfter {
			continue
		}
		down := now.Sub(row.DownSince)
		if down < minFor {
			continue
		}
		detail := fmt.Sprintf("%s/%s (%s): uptime check failing for %s", row.Project, row.Service, row.Env, shortDuration(down))
		if row.LastError != "" {
			detail += " — " + summary(row.LastError, 120)
		} else if row.LastStatusCode != 0 {
			detail += fmt.Sprintf(" — HTTP %d", row.LastStatusCode)
		}
		f.add(row.Namespace+"/"+row.Env, detail)
	}
	sort.Strings(f.targets)
	return f
}
