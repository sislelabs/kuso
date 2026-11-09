package alerts

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

var wlNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func ctrStatus(name string, restarts int32, lastEnded time.Time, waiting string) corev1.ContainerStatus {
	cs := corev1.ContainerStatus{Name: name, RestartCount: restarts}
	if !lastEnded.IsZero() {
		cs.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(lastEnded)}
	}
	if waiting != "" {
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
	}
	return cs
}

func podWith(cs ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: cs}}
}

func TestCrashloopFinding(t *testing.T) {
	t.Parallel()
	envs := []envTarget{
		{name: "shop-api-production", project: "shop", service: "api", env: "production"},
		{name: "shop-web-production", project: "shop", service: "web", env: "production"},
		{name: "shop-worker-production", project: "shop", service: "worker", env: "production"},
		{name: "shop-old-production", project: "shop", service: "old", env: "production"},
	}
	pods := map[string][]*corev1.Pod{
		// backing off right now: fires regardless of count
		"shop-api-production": {podWith(ctrStatus("app", 1, wlNow.Add(-time.Hour), "CrashLoopBackOff"))},
		// healthy
		"shop-web-production": {podWith(ctrStatus("app", 0, time.Time{}, ""))},
		// 4 restarts, the last one 2m ago: fires
		"shop-worker-production": {podWith(ctrStatus("app", 4, wlNow.Add(-2*time.Minute), ""))},
		// 9 restarts but the last one was a day ago: settled, no fire
		"shop-old-production": {podWith(ctrStatus("app", 9, wlNow.Add(-24*time.Hour), ""))},
	}
	f := crashloopFinding(envs, pods, 3, 5*time.Minute, wlNow)
	want := []string{"shop-api-production", "shop-worker-production"}
	if strings.Join(f.targets, ",") != strings.Join(want, ",") {
		t.Fatalf("targets = %v, want %v", f.targets, want)
	}
	if !strings.Contains(f.details["shop-api-production"], "CrashLoopBackOff") {
		t.Errorf("detail = %q", f.details["shop-api-production"])
	}
}

func build(name, project, svc, phase string, completed time.Time) kube.KusoBuild {
	b := kube.KusoBuild{}
	b.Name = name
	b.Spec.Project = project
	b.Spec.Service = project + "-" + svc
	b.Annotations = map[string]string{annBuildPhase: phase, annBuildCompletedAt: completed.Format(time.RFC3339)}
	return b
}

func TestDeployFailedFinding(t *testing.T) {
	t.Parallel()
	builds := []kube.KusoBuild{
		build("api-1", "shop", "api", "succeeded", wlNow.Add(-time.Hour)),
		build("api-2", "shop", "api", "failed", wlNow.Add(-2*time.Minute)),
		build("web-1", "shop", "web", "failed", wlNow.Add(-time.Hour)),
		build("web-2", "shop", "web", "succeeded", wlNow.Add(-time.Minute)),
		build("job-1", "shop", "job", "release-failed", wlNow.Add(-time.Minute)),
		build("stale-1", "shop", "stale", "failed", wlNow.Add(-48*time.Hour)),
		build("gone-1", "shop", "gone", "cancelled", wlNow.Add(-time.Minute)),
		build("blog-1", "blog", "web", "failed", wlNow.Add(-time.Minute)),
	}
	r := &db.AlertRule{Kind: db.AlertKindDeployFailed, Project: "shop", WindowSeconds: 600}
	f := deployFailedFinding(r, builds, wlNow)
	want := []string{"shop/api", "shop/job"}
	if strings.Join(f.targets, ",") != strings.Join(want, ",") {
		t.Fatalf("targets = %v, want %v", f.targets, want)
	}
	if !strings.Contains(f.details["shop/job"], "release hook failed") {
		t.Errorf("release detail = %q", f.details["shop/job"])
	}
	// An already-firing target stays open past the window until a later build succeeds.
	r.FiringTargets = []string{"shop/stale"}
	f = deployFailedFinding(r, builds, wlNow)
	if _, ok := f.details["shop/stale"]; !ok {
		t.Errorf("open episode for shop/stale dropped: %v", f.targets)
	}
}

func TestHealthcheckFinding(t *testing.T) {
	t.Parallel()
	row := func(svc, env string, downFor time.Duration, paused string, checkedAgo time.Duration) db.UptimeState {
		s := db.UptimeState{Namespace: "shop", Project: "shop", Service: svc, Env: env, Paused: paused, LastCheckedAt: wlNow.Add(-checkedAgo), LastStatusCode: 502}
		if downFor > 0 {
			s.DownSince = wlNow.Add(-downFor)
		}
		return s
	}
	rows := []db.UptimeState{
		row("web", "shop-web-production", 10*time.Minute, "", 30*time.Second), // fires
		row("api", "shop-api-production", 2*time.Minute, "", 30*time.Second),  // not long enough
		row("app", "shop-app-production", 0, "", 30*time.Second),               // up
		row("zzz", "shop-zzz-production", time.Hour, "sleeping", 30*time.Second),
		row("old", "shop-old-production", time.Hour, "", time.Hour), // stale row: the loop is down, not the app
	}
	r := &db.AlertRule{Kind: db.AlertKindHealthcheckFailing, Project: "shop"}
	f := healthcheckFinding(r, rows, wlNow)
	if strings.Join(f.targets, ",") != "shop/shop-web-production" {
		t.Fatalf("targets = %v", f.targets)
	}
	if !strings.Contains(f.details["shop/shop-web-production"], "HTTP 502") {
		t.Errorf("detail = %q", f.details["shop/shop-web-production"])
	}
	r.Env = "staging"
	if f := healthcheckFinding(r, rows, wlNow); len(f.targets) != 0 {
		t.Errorf("env scope ignored: %v", f.targets)
	}
}
