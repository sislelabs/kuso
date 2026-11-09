package vulnscan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// A rollout keeps an addon's restart as short as the restart itself:
//
//  1. Pre-pull the new image onto every node the addon runs on, so the
//     restart doesn't include a registry download.
//  2. Postgres (single-node): CHECKPOINT, so shutdown has little to flush.
//  3. Pin the image; the operator re-renders and the pod is replaced.
//  4. Postgres: once the old pod is terminating, PAUSE its PgBouncer
//     (pooled clients wait instead of failing) and disconnect clients,
//     so the shutdown is a clean, immediate one even on a pod that
//     predates the chart's fast-shutdown preStop hook.
//  5. RESUME the pooler once the new pod is Ready (or before PgBouncer's
//     30s query_wait_timeout would fail the queued queries anyway).
//
// HA Postgres needs none of 2-5: CNPG updates replicas first and then
// switches over (primaryUpdateMethod: switchover).

const (
	rolloutBudget   = 12 * time.Minute
	prepullTimeout  = 4 * time.Minute
	terminateWait   = 3 * time.Minute
	defaultRollWait = 5 * time.Minute
	// maxPause stays under the pooler's query_wait_timeout (30s).
	maxPause        = 25 * time.Second
	defaultPollTick = time.Second
)

func (u *AddonUpdater) poll() time.Duration {
	if u.PollEvery > 0 {
		return u.PollEvery
	}
	return defaultPollTick
}

func (u *AddonUpdater) rollWait() time.Duration {
	if u.RolloutWait != 0 {
		return u.RolloutWait
	}
	return defaultRollWait
}

// rollout moves one addon onto st.Latest and records the outcome. It
// runs in the background (detached from the request or tick).
func (u *AddonUpdater) rollout(a kube.KusoAddon, st AddonUpdateState) {
	ctx, cancel := context.WithTimeout(context.Background(), rolloutBudget)
	defer cancel()
	fail := func(msg string) {
		st.Status, st.Message, st.StartedAt = UpdateError, msg, nil
		u.saveOne(ctx, a, st)
		u.log().Warn("addon update: rollout failed", "addon", a.Name, "image", st.Image, "msg", msg)
	}
	pods, err := u.Cluster.Pods(ctx)
	if err != nil {
		fail("list pods: " + err.Error())
		return
	}
	target := ImageRepo(st.Image) + "@" + st.Latest
	if u.Kube != nil {
		if err := u.prepull(ctx, target, nodesOf(mainPods(pods, a))); err != nil {
			fail("couldn't pull the new image, nothing was restarted: " + err.Error())
			return
		}
	}
	st.Message = "restarting"
	u.saveOne(ctx, a, st)

	var pg *pgControl
	if u.Kube != nil && a.Spec.Kind == "postgres" && !a.Spec.HA {
		pg = u.openPG(ctx, a)
	}
	var before []corev1.Pod
	if pg != nil {
		defer pg.close()
		pg.checkpoint(ctx)
		before = mainPods(pods, a)
	}
	if err := u.pin(ctx, a, st.Image, st.Latest); err != nil {
		fail("pin image: " + err.Error())
		return
	}
	pinnedAt := u.now()
	st.AppliedAt = &pinnedAt
	u.emit(a, st, st.Manual)

	if pg != nil {
		if u.waitTerminating(ctx, before) {
			pg.pauseAndDrain(ctx, u.log())
			defer pg.resume(u.log())
			readyCtx, done := context.WithTimeout(ctx, maxPause)
			u.waitReady(readyCtx, a, st.Latest)
			done()
			pg.resume(u.log())
		}
	}
	if u.waitReady(ctx, a, st.Latest) {
		st.Status, st.Message, st.Running = UpdateCurrent, "", st.Latest
	} else {
		// watchRoll in the next ticks finishes it or rolls it back.
		st.Status, st.Message = UpdateRolling, "waiting for pods to become Ready on the new image"
	}
	st.StartedAt = nil
	u.saveOne(ctx, a, st)
}

// mainPods are the addon's own workload pods (see MainImage).
func mainPods(pods []corev1.Pod, a kube.KusoAddon) []corev1.Pod {
	var out []corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.Namespace != a.Namespace || p.Labels[labelAddon] != a.Name || ownedByJob(p) ||
			p.Labels[kube.LabelPrefix+"component"] == "pooler" || p.Labels["cnpg.io/poolerName"] != "" ||
			p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed || len(p.Spec.Containers) == 0 {
			continue
		}
		out = append(out, *p)
	}
	return out
}

func nodesOf(pods []corev1.Pod) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pods {
		if n := p.Spec.NodeName; n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// waitTerminating reports whether one of the pods running before the
// pin started going away: deleting, gone, or replaced by a new pod of
// the same name. It reads the API directly and polls fast, because a
// fast Postgres shutdown can come and go between two informer updates.
func (u *AddonUpdater) waitTerminating(ctx context.Context, before []corev1.Pod) bool {
	if len(before) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, terminateWait)
	defer cancel()
	every := u.poll() / 4
	for {
		for _, old := range before {
			p, err := u.Kube.CoreV1().Pods(old.Namespace).Get(ctx, old.Name, metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(err):
				return true
			case err != nil:
				continue
			case p.UID != old.UID || p.DeletionTimestamp != nil:
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(every):
		}
	}
}

// waitReady reports whether every main pod is Ready on digest before
// ctx or the rollout wait ends.
func (u *AddonUpdater) waitReady(ctx context.Context, a kube.KusoAddon, digest string) bool {
	if u.rollWait() < 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, u.rollWait())
	defer cancel()
	for {
		if pods, err := u.Cluster.Pods(ctx); err == nil {
			if _, running := MainImage(pods, a); running == digest {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(u.poll()):
		}
	}
}

// PrepullJob is a Job that only makes node pull image. Its command may
// not even exist in the image; the pull has happened by the time the
// container fails to start, which is all that matters.
func PrepullJob(namespace, node, image string) *batchv1.Job {
	yes, no := true, false
	uid := int64(65534)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "kuso-prepull-",
			Namespace:    namespace,
			Labels:       map[string]string{kube.LabelPrefix + "prepull": "true"},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   ptr(int64(prepullTimeout.Seconds())),
			TTLSecondsAfterFinished: ptr(int32(300)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{kube.LabelPrefix + "prepull": "true"}},
				Spec: corev1.PodSpec{
					NodeName:                     node,
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &no,
					Tolerations:                  []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &yes,
						RunAsUser:      &uid,
						RunAsGroup:     &uid,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            "pull",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"true"},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &no,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
				},
			},
		},
	}
}

// pullOutcome reads a pre-pull pod: done once the image is on the node,
// err when the pull failed, neither while it's still pulling.
func pullOutcome(p corev1.Pod) (done bool, err error) {
	for _, cs := range p.Status.ContainerStatuses {
		switch {
		case cs.State.Running != nil, cs.State.Terminated != nil, cs.ImageID != "":
			return true, nil
		case cs.State.Waiting != nil:
			switch cs.State.Waiting.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "ErrImageNeverPull", "RegistryUnavailable":
				return false, fmt.Errorf("%s: %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
			case "", "ContainerCreating", "PodInitializing":
				return false, nil
			default:
				// CreateContainerError and friends come after the pull.
				return true, nil
			}
		}
	}
	if p.Status.Phase == corev1.PodFailed {
		return false, fmt.Errorf("pre-pull pod failed: %s %s", p.Status.Reason, p.Status.Message)
	}
	return false, nil
}

func (u *AddonUpdater) prepull(ctx context.Context, image string, nodes []string) error {
	if len(nodes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, prepullTimeout)
	defer cancel()
	jobs := u.Kube.BatchV1().Jobs(u.Namespace)
	var names []string
	defer func() {
		bg := metav1.DeletePropagationBackground
		for _, n := range names {
			_ = jobs.Delete(context.Background(), n, metav1.DeleteOptions{PropagationPolicy: &bg})
		}
	}()
	for _, node := range nodes {
		j, err := jobs.Create(ctx, PrepullJob(u.Namespace, node, image), metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create pre-pull job on %s: %w", node, err)
		}
		names = append(names, j.Name)
	}
	pending := map[string]string{} // job → node
	for i, n := range names {
		pending[n] = nodes[i]
	}
	for len(pending) > 0 {
		for name, node := range pending {
			pl, err := u.Kube.CoreV1().Pods(u.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
			if err != nil {
				return fmt.Errorf("pre-pull on %s: %w", node, err)
			}
			for _, p := range pl.Items {
				done, err := pullOutcome(p)
				if err != nil {
					return fmt.Errorf("pre-pull on %s: %w", node, err)
				}
				if done {
					delete(pending, name)
				}
			}
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pre-pull didn't finish within %s", prepullTimeout)
		case <-time.After(u.poll()):
		}
	}
	return nil
}

// pgControl holds the admin connections a Postgres rollout uses. Every
// step is best-effort: a failure only means a slower, plainer restart.
type pgControl struct {
	dsn    string
	db     *sql.DB // the addon's superuser, kept open across the shutdown
	pooler *sql.DB // PgBouncer admin console, nil without a pooler
	paused bool
}

func qualifyHost(host, ns string) string {
	if host == "" || strings.Contains(host, ".") {
		return host
	}
	return host + "." + ns + ".svc"
}

func pgDSN(host, port, user, pass, dbName string) string {
	esc := func(v string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `'`, `\'`) + "'"
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		esc(host), esc(port), esc(user), esc(pass), esc(dbName))
}

func (u *AddonUpdater) openPG(ctx context.Context, a kube.KusoAddon) *pgControl {
	sec, err := u.Kube.CoreV1().Secrets(a.Namespace).Get(ctx, a.Name+"-conn", metav1.GetOptions{})
	if err != nil {
		u.log().Warn("addon update: read conn secret", "addon", a.Name, "err", err)
		return nil
	}
	get := func(k, def string) string {
		if v := string(sec.Data[k]); v != "" {
			return v
		}
		return def
	}
	pass := get("POSTGRES_PASSWORD", "")
	if pass == "" || a.Spec.TLS == "require" {
		// TLS addons need sslmode=require; skip rather than guess.
		return nil
	}
	user := get("POSTGRES_USER", "kuso")
	dsn := pgDSN(qualifyHost(get("POSTGRES_HOST", a.Name+"-postgresql"), a.Namespace), get("POSTGRES_PORT", "5432"), user, pass, get("POSTGRES_DB", "kuso"))
	c := &pgControl{dsn: dsn}
	if db, err := sql.Open("postgres", dsn); err == nil {
		db.SetMaxOpenConns(1)
		if err := db.PingContext(ctx); err == nil {
			c.db = db
		} else {
			_ = db.Close()
			u.log().Warn("addon update: connect to postgres", "addon", a.Name, "err", err)
		}
	}
	if ph := get("POOLER_HOST", ""); ph != "" {
		dsn := pgDSN(qualifyHost(ph, a.Namespace), get("POOLER_PORT", "6432"), user, pass, "pgbouncer")
		if db, err := sql.Open("postgres", dsn); err == nil {
			db.SetMaxOpenConns(1)
			// Not Ping: lib/pq pings with ";", which the admin console rejects.
			if _, err := db.ExecContext(ctx, "SHOW VERSION"); err == nil {
				c.pooler = db
			} else {
				_ = db.Close()
				u.log().Warn("addon update: connect to pgbouncer admin", "addon", a.Name, "err", err)
			}
		}
	}
	if c.db == nil && c.pooler == nil {
		return nil
	}
	return c
}

func (c *pgControl) checkpoint(ctx context.Context) {
	if c.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, _ = c.db.ExecContext(ctx, "CHECKPOINT")
}

// pauseAndDrain holds pooled clients, then disconnects client sessions
// until Postgres is refusing new connections (it has begun shutting
// down), and finally drops its own session. A smart shutdown waits for
// every session, ours included, so ours has to go last.
func (c *pgControl) pauseAndDrain(ctx context.Context, log interface{ Warn(string, ...any) }) {
	if c.pooler != nil {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := c.pooler.ExecContext(pctx, "PAUSE"); err != nil {
			log.Warn("addon update: pgbouncer PAUSE", "err", err)
		}
		cancel()
		c.paused = true // even a timed-out PAUSE may land; always RESUME
	}
	if c.db == nil {
		return
	}
	defer func() {
		_ = c.db.Close()
		c.db = nil
	}()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.terminateClients(ctx); err != nil {
			return // the server is already gone
		}
		if c.refusingConnections(ctx) {
			// Nobody new can get in: clear whoever slipped in, then leave.
			_ = c.terminateClients(ctx)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (c *pgControl) terminateClients(ctx context.Context) error {
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := c.db.ExecContext(qctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'`)
	return err
}

// refusingConnections reports whether a fresh login is turned away,
// which is how a smart shutdown in progress looks from outside.
func (c *pgControl) refusingConnections(ctx context.Context) bool {
	probe, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return false
	}
	defer probe.Close()
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	err = probe.PingContext(pctx)
	return err != nil && strings.Contains(err.Error(), "shutting down")
}

func (c *pgControl) resume(log interface{ Warn(string, ...any) }) {
	if c.pooler == nil || !c.paused {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.pooler.ExecContext(ctx, "RESUME"); err != nil && !strings.Contains(err.Error(), "not paused") {
		log.Warn("addon update: pgbouncer RESUME", "err", err)
		return
	}
	c.paused = false
}

func (c *pgControl) close() {
	if c.db != nil {
		_ = c.db.Close()
	}
	if c.pooler != nil {
		_ = c.pooler.Close()
	}
}
