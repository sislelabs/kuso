// Package previewdb spins up a per-PR clone of the project's postgres
// addons and seeds them with a pg_dump from the source. Used by the
// github webhook flow: when a PR is opened, every preview env points
// at a fresh per-PR addon (instead of sharing production's), so
// reviewers can break the schema without breaking prod.
//
// Flow per addon:
//  1. Look up source addon's spec; copy size/version/database into a
//     new addon CR named "<source>-pr-<N>".
//  2. Wait for the new addon's helm release to land (StatefulSet
//     pods Ready + the "<name>-conn" Secret to exist).
//  3. Spawn a kube Job that runs `pg_dump <source-conn> | psql <clone-conn>`.
//  4. Returns the list of "<clone>-conn" Secret names so the env
//     creation flow can wire envFromSecrets to point at the clones.
//
// On PR close, DeletePRAddons removes every "<source>-pr-<N>" CR;
// the kusoaddon helm chart's uninstall finalizer cleans up the
// StatefulSet + PVCs.

package previewdb

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

// previewCloneNameRE matches addon short names that follow the
// "<source>-pr-<N>" convention used by EnsurePRAddons. Matches both
// well-formed clones (tickero-pg-pr-35) and the broken accumulated
// names from pre-v0.17.6 sync runs (tickero-pg-pr-35-pr-35-pr-35).
var previewCloneNameRE = regexp.MustCompile(`-pr-\d+(?:-pr-\d+)*$`)

// isPreviewCloneName returns true when shortName ends in a
// "-pr-<N>" segment (possibly repeated). Used to skip addons that
// are themselves preview clones during sync.
func isPreviewCloneName(shortName string) bool {
	return previewCloneNameRE.MatchString(shortName)
}

type Cloner struct {
	Kube      *kube.Client
	Addons    *addons.Service
	Namespace string
	Logger    *slog.Logger
	// BaseCtx is the server's lifecycle context. Background seed
	// jobs derive from this (with a 30-min timeout) so a graceful
	// shutdown cancels in-flight pg_dump pipes instead of leaving
	// detached goroutines and zombie psql processes against a
	// half-torn-down kube client.
	BaseCtx context.Context

	// seedInFlight dedupes concurrent seed+migrate spawns per clone.
	// EnsurePRAddons runs once per service; several services sharing a
	// DB addon would otherwise each spawn a seed+migrate for the same
	// clone. Guarded by seedMu. See tryAcquireSeed/releaseSeed.
	seedMu       sync.Mutex
	seedInFlight map[string]bool
}

func New(ctx context.Context, k *kube.Client, addonSvc *addons.Service, namespace string, logger *slog.Logger) *Cloner {
	if namespace == "" {
		namespace = "kuso"
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Cloner{Kube: k, Addons: addonSvc, Namespace: namespace, Logger: logger, BaseCtx: ctx}
}

// EnsurePRAddons creates per-PR clones for every postgres addon in
// the project + kicks off seed Jobs. Returns the list of clone
// connection-secret names, which callers swap into envFromSecrets.
//
// Idempotent: re-running for the same PR finds the existing clones
// and re-issues seed Jobs (so the reviewer can resync data).
func (c *Cloner) EnsurePRAddons(ctx context.Context, project string, prNumber int) ([]string, error) {
	// Preview behavior, unchanged: postgres-only, seeded from the project's
	// source postgres addon, with the preview-specific source-tracking labels.
	// Everything below is the env-scope-keyed core (EnsureEnvAddons).
	return c.EnsureEnvAddons(ctx, project, fmt.Sprintf("preview-pr-%d", prNumber), EnvAddonOpts{
		Kinds:     []string{"postgres"},
		SeedAll:   true,
		PreviewPR: fmt.Sprintf("%d", prNumber),
	})
}

// EnvAddonOpts controls how EnsureEnvAddons provisions a named env's addons.
type EnvAddonOpts struct {
	// Kinds limits which addon kinds get a per-env instance. Empty = postgres
	// only (the historical preview default). Values: "postgres", "redis", "s3".
	Kinds []string
	// SeedAll, when true, seeds every postgres clone from its SOURCE addon via
	// pg_dump|psql (the preview behavior). When false, postgres clones start
	// EMPTY unless explicitly seeded by a caller-set source — named staging/qa
	// envs default to empty. redis/s3 instances are never seeded.
	SeedAll bool
	// PreviewPR, when non-empty, stamps the preview-specific source-tracking
	// labels (kuso.sislelabs.com/preview-pr + preview-source) so the existing
	// preview-delete sweep keeps working. Empty for named envs.
	PreviewPR string
}

// EnsureEnvAddons creates per-env instances of the project's stateful addons,
// scoped by the kuso.sislelabs.com/env label = envScope, and returns the clones'
// conn-secret names (callers swap these into the env's EnvFromSecrets). Idempotent:
// an existing clone is reused (re-seeded only when SeedAll is set). Postgres clones
// seed from their source when SeedAll is set; redis/s3 instances are always fresh.
func (c *Cloner) EnsureEnvAddons(ctx context.Context, project, envScope string, opts EnvAddonOpts) ([]string, error) {
	if c == nil || c.Addons == nil {
		return nil, nil
	}
	wantKind := func(k string) bool {
		if len(opts.Kinds) == 0 {
			return k == "postgres"
		}
		for _, x := range opts.Kinds {
			if x == k {
				return true
			}
		}
		return false
	}
	ns := c.namespaceFor(ctx, project)
	sources, err := c.Addons.List(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	var connSecrets []string
	for i := range sources {
		s := &sources[i]
		if !wantKind(s.Spec.Kind) {
			continue
		}
		// Skip env-scoped addons (a clone) — never clone a clone. The
		// kuso.sislelabs.com/env label marks an addon as belonging to one
		// specific env; the name-suffix fallback catches pre-label clones.
		if s.Labels[kube.LabelEnv] != "" {
			continue
		}
		shortSrc := addons.ShortName(project, s.Name)
		if isPreviewCloneName(shortSrc) {
			continue
		}
		// Instance-pg addons clone differently (a per-env database on the
		// shared server, not a StatefulSet); addons.Add handles that when
		// UseInstanceAddon is carried through.
		instancePG := s.Spec.UseInstanceAddon != ""
		cloneShort := fmt.Sprintf("%s-%s", shortSrc, envScope)
		cloneFQN := addons.CRName(project, cloneShort)

		extraLabels := map[string]string{kube.LabelEnv: envScope}
		if opts.PreviewPR != "" {
			extraLabels["kuso.sislelabs.com/preview-pr"] = opts.PreviewPR
			extraLabels["kuso.sislelabs.com/preview-source"] = shortSrc
		}

		// Create the clone if it doesn't exist. We don't update an existing
		// clone — re-running just re-seeds it (when SeedAll).
		if existing, _ := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); existing == nil {
			if _, err := c.Addons.Add(ctx, project, addons.CreateAddonRequest{
				Name:    cloneShort,
				Kind:    s.Spec.Kind,
				Version: s.Spec.Version,
				Size:    s.Spec.Size,
				// Don't carry HA — per-env clones stay single-replica
				// regardless of the source's HA setting.
				HA:               false,
				StorageSize:      s.Spec.StorageSize,
				Database:         s.Spec.Database,
				UseInstanceAddon: s.Spec.UseInstanceAddon,
				ExtraLabels:      extraLabels,
			}); err != nil {
				c.Logger.Warn("env addon clone create", "addon", cloneShort, "scope", envScope, "err", err)
				return nil, fmt.Errorf("provision %s for env %s: %w", cloneShort, envScope, err)
			}
			c.Logger.Info("env addon provisioned", "source", shortSrc, "clone", cloneShort, "scope", envScope)
		}
		connSecrets = append(connSecrets, addons.ConnSecretName(cloneFQN))

		// Seed only postgres clones, and only when SeedAll is set (preview).
		// Named envs default to an empty DB.
		if s.Spec.Kind != "postgres" || !opts.SeedAll {
			continue
		}
		// Dedupe: EnsureEnvAddons runs once per service, so several services
		// sharing this DB addon would each spawn a seed+migrate for the same
		// clone. Only the first in-flight spawn proceeds; the conn secret is
		// still returned above, so the env mounts the clone regardless.
		if !c.tryAcquireSeed(cloneFQN) {
			continue
		}
		seedCtx, cancel := context.WithTimeout(c.BaseCtx, 30*time.Minute)
		go func(src, clone string, isInstancePG bool) {
			defer cancel()
			defer c.releaseSeed(clone)
			c.seedAsync(seedCtx, ns, project, src, clone, isInstancePG, envScope)
		}(addons.CRName(project, s.Name), cloneFQN, instancePG)
	}
	return connSecrets, nil
}

// DeletePRAddons removes every "*-pr-<N>" addon CR for the project.
// Helm-operator's uninstall finalizer drops the StatefulSet + PVCs.
func (c *Cloner) DeletePRAddons(ctx context.Context, project string, prNumber int) error {
	if c == nil || c.Addons == nil {
		return nil
	}
	suffix := fmt.Sprintf("-pr-%d", prNumber)
	all, err := c.Addons.List(ctx, project)
	if err != nil {
		return fmt.Errorf("list addons: %w", err)
	}
	var firstErr error
	for i := range all {
		a := &all[i]
		short := addons.ShortName(project, a.Name)
		if !strings.HasSuffix(short, suffix) {
			continue
		}
		if err := c.Addons.Delete(ctx, project, short); err != nil {
			c.Logger.Warn("preview db clone delete", "addon", short, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c.Logger.Info("preview db clone deleted", "addon", short)
	}
	return firstErr
}

// seedAsync waits for the clone's StatefulSet to be ready, then
// spawns a Job that pg_dumps from the source into the clone. Best-
// effort: failures are logged; the preview env still boots, just
// with an empty DB.
func (c *Cloner) seedAsync(ctx context.Context, ns, project, sourceFQN, cloneFQN string, instancePG bool, envScope string) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if c.cloneReady(ctx, ns, cloneFQN, instancePG) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !c.cloneReady(ctx, ns, cloneFQN, instancePG) {
		c.Logger.Warn("preview db clone never reached ready", "clone", cloneFQN)
		return
	}
	// Instance-pg clones provision their DB + role synchronously in
	// addons.Add (against the shared server's admin DSN), so the conn
	// secret's password is already correct — there's no StatefulSet pod
	// with a local trust socket to RepairPassword against. Skip the
	// repair step (which targets native-addon pods) for them.
	if instancePG {
		if err := c.seedAndMigrate(ctx, ns, project, sourceFQN, cloneFQN, envScope); err != nil {
			c.Logger.Warn("preview db seed job (instance-pg)", "clone", cloneFQN, "err", err)
			return
		}
		c.Logger.Info("preview db clone seeded (instance-pg)", "clone", cloneFQN)
		return
	}
	// Align the clone role's password to its conn secret before seeding.
	// The kusoaddon chart can leave the conn-secret POSTGRES_PASSWORD out
	// of sync with the password the role actually has (the same drift
	// `kuso project addon repair-password` recovers from) — the clone is
	// especially exposed because it's created + reconciled in rapid
	// succession by this background goroutine. On drift, the seed Job's
	// destination psql SASL-fails AND every preview pod that reads the
	// conn secret can't reach its DB ("password authentication failed for
	// user kuso"). RepairPassword runs ALTER USER over the local trust
	// socket; idempotent when already aligned. Non-fatal — if no drift
	// occurred the seed + pods work regardless.
	cloneShort := addons.ShortName(project, cloneFQN)
	if err := c.Addons.RepairPassword(ctx, project, cloneShort); err != nil {
		c.Logger.Warn("preview db clone repair-password", "clone", cloneFQN, "err", err)
	}
	if err := c.seedAndMigrate(ctx, ns, project, sourceFQN, cloneFQN, envScope); err != nil {
		c.Logger.Warn("preview db seed job", "clone", cloneFQN, "err", err)
		return
	}
	c.Logger.Info("preview db clone seeded", "clone", cloneFQN)
}

func (c *Cloner) cloneReady(ctx context.Context, ns, cloneFQN string, instancePG bool) bool {
	// The conn-secret "<release>-conn" must exist either way — that's
	// what the seed Job + preview pods read.
	connName := addons.ConnSecretName(cloneFQN)
	if _, err := c.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connName, metav1.GetOptions{}); err != nil {
		return false
	}
	// Instance-pg clones have NO StatefulSet — the DB lives on the
	// shared server, provisioned synchronously by addons.Add. The conn
	// secret existing is sufficient readiness.
	if instancePG {
		return true
	}
	// Native clones: the kusoaddon chart names the StatefulSet
	// "<release>"; wait for a ready replica before pg_dump | psql.
	ss, err := c.Kube.Clientset.AppsV1().StatefulSets(ns).Get(ctx, cloneFQN, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return ss.Status.ReadyReplicas >= 1
}

// runSeedJob creates the seed Job and returns its name + the per-run nonce
// (the same nonce keys the post-seed migrate Job, so a re-seed → re-migrate).
// jobName is "" when the create was a no-op dedupe (Job already in flight).
func (c *Cloner) runSeedJob(ctx context.Context, ns, project, sourceFQN, cloneFQN string) (jobName string, nonce int64, err error) {
	// Own the Job by the clone addon CR so kube-GC cascades the delete
	// when DeletePRAddons drops the clone on PR-close (mirrors how
	// addons.Add owns each addon by its KusoProject). Best-effort: if the
	// clone CR lookup fails we still create the Job (the TTL inside
	// buildSeedJob is the fallback reaper), we just lose the cascade.
	var ownerUID types.UID
	if clone, err := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); err == nil && clone != nil {
		ownerUID = clone.UID
	}

	nonce = time.Now().Unix()
	job := buildSeedJob(ns, project, sourceFQN, cloneFQN, ownerUID, nonce)
	if _, err := c.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return "", nonce, nil // re-running for the same PR; previous Job
			// either succeeded or is still in flight.
		}
		return "", nonce, fmt.Errorf("create seed job: %w", err)
	}
	return job.Name, nonce, nil
}

// seedAndMigrate runs the seed Job, waits for it to complete, then runs the
// post-seed migration against the clone for every preview env that uses it.
// The migrate is strictly ordered after the seed by construction — this is the
// fix for the close→reopen bug where a re-seed dropped a migration that a
// build-promote-time release had applied earlier. prNumber drives the env
// lookup; project/clone identify the DB.
func (c *Cloner) seedAndMigrate(ctx context.Context, ns, project, sourceFQN, cloneFQN string, envScope string) error {
	jobName, nonce, err := c.runSeedJob(ctx, ns, project, sourceFQN, cloneFQN)
	if err != nil {
		return err
	}
	// Wait for the seed to actually finish before migrating — the seed's
	// `pg_dump --clean` drops+recreates tables, so a migration that ran
	// before/during the seed would be wiped. When jobName is "" the create
	// deduped (a seed for this PR is already in flight); wait on the latest
	// seed Job for this clone instead.
	waitName := jobName
	if waitName == "" {
		waitName = c.latestSeedJobName(ctx, ns, project, cloneFQN)
	}
	if waitName != "" {
		if werr := c.waitForJobComplete(ctx, ns, waitName, 5*time.Minute); werr != nil {
			c.Logger.Warn("preview seed job did not complete; skipping migrate", "clone", cloneFQN, "job", waitName, "err", werr)
			return nil
		}
	}
	c.migrateAfterSeed(ctx, ns, project, envScope, cloneFQN, nonce)
	return nil
}

// latestSeedJobName returns the most-recently-created preview-seed Job name for
// this clone, or "" if none. Used when runSeedJob deduped against an in-flight
// seed and we still need something to wait on.
func (c *Cloner) latestSeedJobName(ctx context.Context, ns, project, cloneFQN string) string {
	jobs, err := c.Kube.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("kuso.sislelabs.com/role=preview-seed,kuso.sislelabs.com/clone-addon=%s", addons.ShortName(project, cloneFQN)),
	})
	if err != nil || len(jobs.Items) == 0 {
		return ""
	}
	latest := jobs.Items[0]
	for i := range jobs.Items {
		if jobs.Items[i].CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = jobs.Items[i]
		}
	}
	return latest.Name
}

// buildSeedJob renders the pg_dump|psql seed Job that clones a source
// postgres addon into a per-PR clone. Pure (no I/O) so the env-var
// sourcing, owner cascade, and TTL stay unit-testable — these are
// exactly the fields that broke (the "-postgresql" host suffix +
// hardcoded "kuso" DB caused every seed to fail DNS resolution, and a
// missing owner/TTL orphaned 27 Failed Jobs). ownerUID empty → no
// owner ref (cascade lost, TTL still reaps). nowUnix makes the Job
// name deterministic in tests.
func buildSeedJob(ns, project, sourceFQN, cloneFQN string, ownerUID types.UID, nowUnix int64) *batchv1.Job {
	jobName := fmt.Sprintf("%s-seed-from-%s-%d", cloneFQN, addons.ShortName(project, sourceFQN), nowUnix)
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	one := int32(1)
	// A couple of retries: the script now waits (pg_isready) for both
	// DBs before dumping, so it shouldn't fail transiently — but if it
	// somehow does, retry rather than leaving an empty preview DB on a
	// one-off blip. The TTL below still reaps the Job either way.
	backoff := int32(2)
	// TTL-reap the Job (and its pod) 1h after it finishes — success or
	// failure. Without this a Failed seed Job sits forever; we saw 27
	// orphaned Failed Jobs accumulate across a PR's resyncs because
	// nothing GC'd them. The ownerReference below cascades the delete on
	// PR-close; this TTL handles the stale-resync case while the PR is
	// still open.
	ttl := int32(3600)

	var owners []metav1.OwnerReference
	if ownerUID != "" {
		// BlockOwnerDeletion / Controller=false for the same reasons as
		// addons.Add: don't deadlock the clone addon's helm-uninstall
		// finalizer behind Job GC, and let helm-operator stay the
		// reconcile controller.
		blockFalse := false
		controllerFalse := false
		owners = append(owners, metav1.OwnerReference{
			APIVersion:         "application.kuso.sislelabs.com/v1alpha1",
			Kind:               "KusoAddon",
			Name:               cloneFQN,
			UID:                ownerUID,
			BlockOwnerDeletion: &blockFalse,
			Controller:         &controllerFalse,
		})
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       ns,
			OwnerReferences: owners,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":    "kuso-server",
				"kuso.sislelabs.com/role":         "preview-seed",
				"kuso.sislelabs.com/project":      project,
				"kuso.sislelabs.com/source-addon": addons.ShortName(project, sourceFQN),
				"kuso.sislelabs.com/clone-addon":  addons.ShortName(project, cloneFQN),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			Completions:             &one,
			Parallelism:             &one,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:            "seed",
						Image:           "ghcr.io/sislelabs/kuso-backup:latest",
						ImagePullPolicy: corev1.PullIfNotPresent,
						Command:         []string{"sh", "-c"},
						// pg_dump from the source piped through psql
						// into the clone. --no-owner avoids role
						// mismatches; --clean ensures we don't error
						// on existing schemas in case the clone
						// helm-template happened to seed default
						// tables.
						Args: []string{`
set -e
# Wait for BOTH the source and the freshly-created clone Postgres to
# actually accept connections before dumping. A per-PR clone is a brand-
# new StatefulSet — its pod can be "ready" (the SS reports a ready
# replica) a beat before Postgres is listening on TCP, and the source
# can blip too. Without this wait the dump raced startup and died with
# "Connection refused"; with BackoffLimit=0 that was a permanent
# failure → an EMPTY preview DB. Poll pg_isready up to ~3 min each.
wait_pg() { # host port user
  i=0
  until pg_isready -h "$1" -p "$2" -U "$3" -q; do
    i=$((i+1))
    if [ "$i" -ge 90 ]; then echo "==> $1:$2 never became ready" >&2; exit 1; fi
    echo "==> waiting for $1:$2 ($i)…"; sleep 2
  done
}
echo "==> waiting for source ${SRC_HOST}:${SRC_PORT:-5432} and clone ${DST_HOST}:${DST_PORT:-5432}"
PGPASSWORD="${SRC_PASSWORD}" wait_pg "${SRC_HOST}" "${SRC_PORT:-5432}" "${SRC_USER}"
PGPASSWORD="${DST_PASSWORD}" wait_pg "${DST_HOST}" "${DST_PORT:-5432}" "${DST_USER}"
echo "==> dumping ${SRC_HOST}/${SRC_DB} → ${DST_HOST}/${DST_DB}"
PGPASSWORD="${SRC_PASSWORD}" pg_dump --no-owner --no-acl --clean --if-exists \
  -h "${SRC_HOST}" -U "${SRC_USER}" "${SRC_DB}" \
  | PGPASSWORD="${DST_PASSWORD}" psql -v ON_ERROR_STOP=1 \
  -h "${DST_HOST}" -U "${DST_USER}" "${DST_DB}"
echo "==> done"
`},
						// Source HOST/USER/DB from each addon's -conn Secret
						// rather than constructing them. The Service name is
						// just the addon CR name (HA writes "<name>-rw" into
						// POSTGRES_HOST), and the DB name falls back to the
						// project name — NOT a literal "kuso". The old
						// "<name>-postgresql" host + hardcoded "kuso" DB was
						// the same bug the backup CronJob already fixed (see
						// kusoaddon/templates/backup-cronjob.yaml v0.7.x note):
						// every seed Job failed with "could not translate host
						// name ... Name does not resolve".
						Env: []corev1.EnvVar{
							envFromSecret("SRC_HOST", addons.ConnSecretName(sourceFQN), "POSTGRES_HOST"),
							envFromSecretOptional("SRC_PORT", addons.ConnSecretName(sourceFQN), "POSTGRES_PORT"),
							envFromSecret("SRC_USER", addons.ConnSecretName(sourceFQN), "POSTGRES_USER"),
							envFromSecret("SRC_DB", addons.ConnSecretName(sourceFQN), "POSTGRES_DB"),
							envFromSecret("SRC_PASSWORD", addons.ConnSecretName(sourceFQN), "POSTGRES_PASSWORD"),
							envFromSecret("DST_HOST", addons.ConnSecretName(cloneFQN), "POSTGRES_HOST"),
							envFromSecretOptional("DST_PORT", addons.ConnSecretName(cloneFQN), "POSTGRES_PORT"),
							envFromSecret("DST_USER", addons.ConnSecretName(cloneFQN), "POSTGRES_USER"),
							envFromSecret("DST_DB", addons.ConnSecretName(cloneFQN), "POSTGRES_DB"),
							envFromSecret("DST_PASSWORD", addons.ConnSecretName(cloneFQN), "POSTGRES_PASSWORD"),
						},
					}},
				},
			},
		},
	}
}

func (c *Cloner) namespaceFor(ctx context.Context, project string) string {
	if c.Addons != nil && c.Addons.NSResolver != nil {
		return c.Addons.NSResolver.NamespaceFor(ctx, project)
	}
	return c.Namespace
}

// envFromSecret builds a secretKeyRef-shaped EnvVar.
func envFromSecret(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: name,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  key,
			},
		},
	}
}

// envFromSecretOptional is envFromSecret with Optional=true — a missing
// key leaves the env var unset (the script supplies a default) instead
// of wedging the pod in CreateContainerConfigError. Used for POSTGRES_
// PORT, which not every addon's conn secret carries.
func envFromSecretOptional(name, secretName, key string) corev1.EnvVar {
	opt := true
	e := envFromSecret(name, secretName, key)
	e.ValueFrom.SecretKeyRef.Optional = &opt
	return e
}
