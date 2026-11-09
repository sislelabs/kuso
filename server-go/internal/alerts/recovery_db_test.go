package alerts

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/db"
	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
	"kuso/server/internal/notify"
)

func rolloutPod(env, name, hash string, age time.Duration, waiting string) *corev1.Pod {
	ready := corev1.ConditionTrue
	cs := corev1.ContainerStatus{Name: "app", Ready: true}
	if waiting != "" {
		ready = corev1.ConditionFalse
		cs = corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: waiting, Message: `failed to pull image "registry/shop-api:typo": not found`,
		}}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: env + "-" + name, Namespace: "kuso",
			Labels:            map[string]string{"app.kubernetes.io/instance": env, "pod-template-hash": hash, kube.LabelProject: "shop"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}},
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}

func attentionTitles(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.ListAttentionNotificationEvents(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Title)
	}
	return out
}

// The bad-tag episode end to end: a new image tag can't be pulled while
// the old version keeps serving, the user reverts, the service recovers.
// Both signals it raised (the rollout_failing alert and the health
// watcher's crash row) must close, and "Needs attention" must empty.
func TestRevertedBadRolloutLeavesNothingNeedingAttention(t *testing.T) {
	d := openAlertsTestDB(t)
	ctx := context.Background()
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-rollout", Name: "Rollout failing", Enabled: true, Kind: db.AlertKindRolloutFailing,
		Project: "shop", Service: "api", Severity: "error", ThrottleSeconds: 1,
	})
	k := fakeKube(shopEnvs()...)
	pods := k.Clientset.CoreV1().Pods("kuso")
	for _, p := range []*corev1.Pod{rolloutPod("shop-api-production", "old", "a", time.Hour, ""), rolloutPod("shop-api-production", "new", "b", 2*time.Minute, "ImagePullBackOff")} {
		if _, err := pods.Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEngine(t, d, k)

	e.tick(ctx)
	cl := failures.Classify(nil, failures.Signal{Reason: "ImagePullBackOff", Runtime: true})
	e.Notify.Emit(notify.PodCrashed(notify.PodCrash{
		Project: "shop", Service: "api", Env: "production", Pod: "new", Reason: "ImagePullBackOff", Classification: &cl,
	}))
	if got := attentionTitles(t, d); len(got) != 2 {
		t.Fatalf("while failing, attention = %v, want the alert and the crash", got)
	}

	// Revert: the new ReplicaSet is scaled away, the old pod serves alone.
	if err := pods.Delete(ctx, "shop-api-production-new", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.tick(ctx)
	e.Notify.Emit(notify.PodRecovered("shop", "api", "production", 4*time.Minute))

	events := alertEvents(t, d)
	if len(events) != 2 || !strings.HasPrefix(events[1], "✓ Resolved") {
		t.Fatalf("alert events = %v, want fire then resolved", events)
	}
	if got := attentionTitles(t, d); len(got) != 0 {
		t.Fatalf("after recovery, attention = %v, want empty", got)
	}
	if n, err := d.CountUnreadNotificationEventsForUser(ctx, "u1", true); err != nil || n != 0 {
		t.Fatalf("attention badge = %d (err %v), want 0", n, err)
	}
}

// A project-wide rule firing for two services: reverting one must clear
// that service's row while the other keeps the rule (and its row) open.
func TestPartialRecoveryClearsOnlyThatTargetsRow(t *testing.T) {
	d := openAlertsTestDB(t)
	ctx := context.Background()
	mustCreateRule(t, d, db.AlertRule{
		ID: "r-rollout", Name: "Rollout failing", Enabled: true, Kind: db.AlertKindRolloutFailing,
		Project: "shop", Severity: "error", ThrottleSeconds: 1,
	})
	k := fakeKube(shopEnvs()...)
	pods := k.Clientset.CoreV1().Pods("kuso")
	create := func(p *corev1.Pod) {
		t.Helper()
		if _, err := pods.Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	create(rolloutPod("shop-api-production", "old", "a", time.Hour, ""))
	create(rolloutPod("shop-api-production", "new", "b", 2*time.Minute, "ImagePullBackOff"))
	e := newTestEngine(t, d, k)
	e.tick(ctx)

	time.Sleep(1100 * time.Millisecond) // past the 1s flap cooldown
	create(rolloutPod("shop-web-production", "old", "a", time.Hour, ""))
	create(rolloutPod("shop-web-production", "new", "b", 2*time.Minute, "ErrImagePull"))
	e.tick(ctx)
	if got := attentionTitles(t, d); len(got) != 2 {
		t.Fatalf("both failing: attention = %v, want two fire rows", got)
	}

	if err := pods.Delete(ctx, "shop-api-production-new", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.tick(ctx)
	rows, err := d.ListAttentionNotificationEvents(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Extra["targets"] != "shop-web-production" {
		t.Fatalf("after reverting api, attention = %+v, want only web's row", rows)
	}

	if err := pods.Delete(ctx, "shop-web-production-new", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	e.tick(ctx)
	if got := attentionTitles(t, d); len(got) != 0 {
		t.Fatalf("all recovered: attention = %v, want empty", got)
	}
}
