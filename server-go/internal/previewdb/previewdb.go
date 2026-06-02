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
	if c == nil || c.Addons == nil {
		return nil, nil
	}
	ns := c.namespaceFor(ctx, project)
	sources, err := c.Addons.List(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	var connSecrets []string
	for i := range sources {
		s := &sources[i]
		// Only postgres for now — redis state is usually ephemeral
		// (cache), and seeding it would mean RDB snapshot transfer.
		// Out of scope.
		if s.Spec.Kind != "postgres" {
			continue
		}
		// Skip env-scoped addons. The kuso.sislelabs.com/env label
		// marks an addon as belonging to one specific env (preview,
		// staging, etc.) rather than being part of the project's
		// source addon set — we use the same label everywhere else
		// in kuso for env scoping, so reading it here keeps the
		// invariant single-source. Without this filter EnsurePRAddons
		// would clone its own clones each sync, producing
		// "tickero-pg-pr-35-pr-35-pr-35" growth per resync.
		//
		// The name-suffix fallback (isPreviewCloneName) catches
		// pre-v0.17.7 clones that were stamped with the bespoke
		// "preview-pr" label instead of the canonical env label;
		// the env-label code-path takes precedence on new clones.
		if s.Labels[kube.LabelEnv] != "" {
			continue
		}
		shortName := addons.ShortName(project, s.Name)
		if isPreviewCloneName(shortName) {
			continue
		}
		// Instance-pg addons (project consumes the cluster-shared PG via
		// spec.useInstanceAddon) clone differently from native ones:
		// there's no StatefulSet to spin up. Instead addons.Add provisions
		// a SEPARATE per-PR database on the shared server (CREATE DATABASE
		// "<project>_<addon>_pr_N" + a role) and writes a <clone>-conn
		// secret — so the preview gets an isolated DB a reviewer can't use
		// to corrupt prod. The seed Job then pg_dump|psql's the source DB
		// into it, same as native clones (it reads host/db/user from the
		// conn secrets, which are reachable shared-server endpoints).
		instancePG := s.Spec.UseInstanceAddon != ""
		shortSrc := addons.ShortName(project, s.Name)
		cloneShort := fmt.Sprintf("%s-pr-%d", shortSrc, prNumber)
		cloneFQN := addons.CRName(project, cloneShort)

		// Create the clone if it doesn't exist. We don't update an
		// existing clone — re-running just re-seeds it.
		if existing, _ := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); existing == nil {
			if _, err := c.Addons.Add(ctx, project, addons.CreateAddonRequest{
				Name:    cloneShort,
				Kind:    s.Spec.Kind,
				Version: s.Spec.Version,
				Size:    s.Spec.Size,
				// Don't carry HA — preview clones stay single-replica
				// regardless of the source's HA setting (cost +
				// streaming-replica setup isn't worth it).
				HA:          false,
				StorageSize: s.Spec.StorageSize,
				Database:    s.Spec.Database,
				// Carry the instance-addon mode so the clone provisions a
				// per-PR database on the shared server instead of a
				// StatefulSet. Empty for native addons (unchanged path).
				UseInstanceAddon: s.Spec.UseInstanceAddon,
				ExtraLabels: map[string]string{
					// Use the canonical env-scope label that the rest of
					// kuso reads (envs, per-env Secrets, the addon-list
					// filter above). Setting it here means EnsurePRAddons
					// won't try to re-clone its own output on the next
					// sync, AND the env-delete sweep can find every
					// clone via a single label-selector List query.
					kube.LabelEnv: fmt.Sprintf("preview-pr-%d", prNumber),
					// Source-tracking label is preview-specific (the
					// canonical env label can't carry both env + source
					// in one field); keeps the preview-delete sweep
					// independent of name parsing.
					"kuso.sislelabs.com/preview-pr":     fmt.Sprintf("%d", prNumber),
					"kuso.sislelabs.com/preview-source": shortSrc,
				},
			}); err != nil {
				c.Logger.Warn("preview db clone create", "addon", cloneShort, "err", err)
				continue
			}
			c.Logger.Info("preview db clone created", "source", shortSrc, "clone", cloneShort)
		}
		connSecrets = append(connSecrets, addons.ConnSecretName(cloneFQN))
		// Kick off the seed Job in a goroutine — the create above
		// returns when the CR lands, but the StatefulSet takes a
		// few seconds to come up. We poll for the pod-ready state
		// inside seedAsync.
		// 30-min cap is plenty for any production-sized DB clone;
		// past that the operator probably wants to know.
		seedCtx, cancel := context.WithTimeout(c.BaseCtx, 30*time.Minute)
		go func(src, clone string, isInstancePG bool) {
			defer cancel()
			c.seedAsync(seedCtx, ns, project, src, clone, isInstancePG)
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
func (c *Cloner) seedAsync(ctx context.Context, ns, project, sourceFQN, cloneFQN string, instancePG bool) {
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
		if err := c.runSeedJob(ctx, ns, project, sourceFQN, cloneFQN); err != nil {
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
	if err := c.runSeedJob(ctx, ns, project, sourceFQN, cloneFQN); err != nil {
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

func (c *Cloner) runSeedJob(ctx context.Context, ns, project, sourceFQN, cloneFQN string) error {
	// Own the Job by the clone addon CR so kube-GC cascades the delete
	// when DeletePRAddons drops the clone on PR-close (mirrors how
	// addons.Add owns each addon by its KusoProject). Best-effort: if the
	// clone CR lookup fails we still create the Job (the TTL inside
	// buildSeedJob is the fallback reaper), we just lose the cascade.
	var ownerUID types.UID
	if clone, err := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); err == nil && clone != nil {
		ownerUID = clone.UID
	}

	job := buildSeedJob(ns, project, sourceFQN, cloneFQN, ownerUID, time.Now().Unix())
	if _, err := c.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil // re-running for the same PR; previous Job
			// either succeeded or is still in flight.
		}
		return fmt.Errorf("create seed job: %w", err)
	}
	return nil
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
