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
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

type Cloner struct {
	Kube      *kube.Client
	Addons    *addons.Service
	Namespace string
	Logger    *slog.Logger
}

func New(k *kube.Client, addonSvc *addons.Service, namespace string, logger *slog.Logger) *Cloner {
	if namespace == "" {
		namespace = "kuso"
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Cloner{Kube: k, Addons: addonSvc, Namespace: namespace, Logger: logger}
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
		go c.seedAsync(context.Background(), ns, project, addons.CRName(project, s.Name), cloneFQN)
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
func (c *Cloner) seedAsync(ctx context.Context, ns, project, sourceFQN, cloneFQN string) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if c.cloneReady(ctx, ns, cloneFQN) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !c.cloneReady(ctx, ns, cloneFQN) {
		c.Logger.Warn("preview db clone never reached ready", "clone", cloneFQN)
		return
	}
	if err := c.runSeedJob(ctx, ns, project, sourceFQN, cloneFQN); err != nil {
		c.Logger.Warn("preview db seed job", "clone", cloneFQN, "err", err)
		return
	}
	c.Logger.Info("preview db clone seeded", "clone", cloneFQN)
}

func (c *Cloner) cloneReady(ctx context.Context, ns, cloneFQN string) bool {
	// The kusoaddon helm chart names the StatefulSet "<release>" and
	// the conn-secret "<release>-conn". Both must exist before we
	// run pg_dump | psql against it.
	connName := addons.ConnSecretName(cloneFQN)
	if _, err := c.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connName, metav1.GetOptions{}); err != nil {
		return false
	}
	ss, err := c.Kube.Clientset.AppsV1().StatefulSets(ns).Get(ctx, cloneFQN, metav1.GetOptions{})
	if err != nil {
		return false
	}
	return ss.Status.ReadyReplicas >= 1
}

func (c *Cloner) runSeedJob(ctx context.Context, ns, project, sourceFQN, cloneFQN string) error {
	jobName := fmt.Sprintf("%s-seed-from-%s-%d", cloneFQN, addons.ShortName(project, sourceFQN), time.Now().Unix())
	if len(jobName) > 63 {
		jobName = jobName[:63]
	}
	one := int32(1)
	zero := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":  "kuso-server",
				"kuso.sislelabs.com/role":       "preview-seed",
				"kuso.sislelabs.com/project":    project,
				"kuso.sislelabs.com/source-addon": addons.ShortName(project, sourceFQN),
				"kuso.sislelabs.com/clone-addon":  addons.ShortName(project, cloneFQN),
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &zero,
			Completions:  &one,
			Parallelism:  &one,
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
echo "==> dumping ${SRC_HOST}/${SRC_DB} → ${DST_HOST}/${DST_DB}"
PGPASSWORD="${SRC_PASSWORD}" pg_dump --no-owner --no-acl --clean --if-exists \
  -h "${SRC_HOST}" -U "${SRC_USER}" "${SRC_DB}" \
  | PGPASSWORD="${DST_PASSWORD}" psql \
  -h "${DST_HOST}" -U "${DST_USER}" "${DST_DB}"
echo "==> done"
`},
						Env: []corev1.EnvVar{
							{Name: "SRC_HOST", Value: sourceFQN + "-postgresql"},
							{Name: "SRC_USER", Value: "kuso"},
							{Name: "SRC_DB", Value: "kuso"},
							envFromSecret("SRC_PASSWORD", addons.ConnSecretName(sourceFQN), "POSTGRES_PASSWORD"),
							{Name: "DST_HOST", Value: cloneFQN + "-postgresql"},
							{Name: "DST_USER", Value: "kuso"},
							{Name: "DST_DB", Value: "kuso"},
							envFromSecret("DST_PASSWORD", addons.ConnSecretName(cloneFQN), "POSTGRES_PASSWORD"),
						},
					}},
				},
			},
		},
	}
	if _, err := c.Kube.Clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil // re-running for the same PR; previous Job
			           // either succeeded or is still in flight.
		}
		return fmt.Errorf("create seed job: %w", err)
	}
	return nil
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
