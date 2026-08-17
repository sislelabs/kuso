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
	"k8s.io/apimachinery/pkg/runtime"
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
		Kinds:   []string{"postgres"},
		SeedAll: true,
		// Keep the historical clone name "<base>-pr-N" (DeletePRAddons + the
		// canvas's -pr-N regex depend on it), even though the env-label scope
		// is "preview-pr-N".
		NameSuffix: fmt.Sprintf("-pr-%d", prNumber),
		PreviewPR:  fmt.Sprintf("%d", prNumber),
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
	// NameSuffix overrides the clone NAME suffix ("<base><NameSuffix>"). It
	// decouples the clone name from the env-label scope: PR previews keep their
	// historical "<base>-pr-N" name while being labeled env=preview-pr-N (the
	// -pr-N suffix is a contract for DeletePRAddons + the canvas regex). Empty
	// = use "-<envScope>" (named envs: "<base>-staging").
	NameSuffix string
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
		suffix := opts.NameSuffix
		if suffix == "" {
			suffix = "-" + envScope
		}
		cloneShort := shortSrc + suffix
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
				// Carry TLS: a tls=require source must clone into a
				// tls=require preview DB, or the clone's conn secret
				// regresses to sslmode=disable and apps that mandate
				// encrypted DB connections in production crashloop in
				// previews (previews inherit ENVIRONMENT via shared
				// secrets).
				TLS: s.Spec.TLS,
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
		// Durable in-flight marker, stamped BEFORE the async work starts.
		// The seed runs in a plain goroutine; if the server restarts
		// between here and seed completion, this annotation is the only
		// evidence a seed is still owed. ResumePendingSeeds sweeps for it
		// on leader acquisition and re-kicks the seed; seedAsync clears it
		// once the seed lands.
		c.markSeedPending(ctx, ns, cloneFQN, addons.CRName(project, s.Name))
		seedCtx, cancel := context.WithTimeout(c.BaseCtx, 30*time.Minute)
		go func(src, clone string, isInstancePG bool) {
			defer cancel()
			defer c.releaseSeed(clone)
			c.seedAsync(seedCtx, ns, project, src, clone, isInstancePG, envScope)
		}(addons.CRName(project, s.Name), cloneFQN, instancePG)
	}
	return connSecrets, nil
}

// previewPRLabel is the ownership label EnsureEnvAddons stamps on a
// per-PR postgres clone (value = the PR number). It's the authoritative
// signal that an addon is a preview clone for a given PR — the name
// suffix alone is NOT, because a real project addon can legitimately be
// named "events-pr-2".
const previewPRLabel = "kuso.sislelabs.com/preview-pr"

// seedPendingAnnotation is the durable "a seed is owed" marker on a clone
// addon CR. Value = the SOURCE addon's FQN (the one input to seedAsync
// that can't be re-derived from the clone CR itself; project/env-scope/
// instance-pg all can). Stamped by EnsureEnvAddons BEFORE the async seed
// goroutine starts, cleared by seedAsync after the seed lands. A server
// restart mid-seed leaves it behind, and ResumePendingSeeds re-kicks any
// clone still carrying it.
const seedPendingAnnotation = "kuso.sislelabs.com/seed-pending"

// markSeedPending stamps the durable in-flight seed marker. Best-effort:
// a failed stamp only costs the restart-resume guarantee for this one
// seed attempt, so it must not block the seed itself.
func (c *Cloner) markSeedPending(ctx context.Context, ns, cloneFQN, sourceFQN string) {
	if _, err := c.Kube.UpdateKusoAddonWithRetry(ctx, ns, cloneFQN, func(a *kube.KusoAddon) error {
		if a.Annotations == nil {
			a.Annotations = map[string]string{}
		}
		a.Annotations[seedPendingAnnotation] = sourceFQN
		return nil
	}); err != nil {
		c.Logger.Warn("seed-pending mark failed; a restart during this seed will not auto-resume",
			"clone", cloneFQN, "ns", ns, "err", err)
	}
}

// clearSeedPending removes the marker once a seed has completed. NotFound
// is fine — the clone may have been torn down (PR closed) mid-seed.
func (c *Cloner) clearSeedPending(ctx context.Context, ns, cloneFQN string) {
	if _, err := c.Kube.UpdateKusoAddonWithRetry(ctx, ns, cloneFQN, func(a *kube.KusoAddon) error {
		delete(a.Annotations, seedPendingAnnotation)
		return nil
	}); err != nil && !apierrors.IsNotFound(err) {
		c.Logger.Warn("seed-pending clear failed; boot resume will re-check this clone",
			"clone", cloneFQN, "ns", ns, "err", err)
	}
}

// ResumePendingSeeds is the boot-time (leader-acquisition-time) resume
// sweep for interrupted clone seeds. seedAsync runs in a plain goroutine
// with in-memory state only; if the server restarts between clone-addon
// creation and seed completion, the half-seeded clone used to stay empty
// until the next PR resync event. This sweep discovers every clone still
// carrying the seed-pending annotation and re-kicks seedAsync for it.
//
// Idempotent: a clone whose latest seed Job already succeeded (crash
// landed between Job completion and marker clear) is NOT re-seeded —
// re-running pg_dump --clean would wipe post-seed release-hook
// migrations — its stale marker is just cleared. Returns the clone FQNs
// whose seeds were re-kicked (used by tests; callers may ignore it).
func (c *Cloner) ResumePendingSeeds(ctx context.Context) []string {
	if c == nil || c.Kube == nil || c.Kube.Dynamic == nil || c.Addons == nil {
		return nil
	}
	// Cluster-wide list: clones live in each project's namespace
	// (KusoProject.spec.namespace), not only the home namespace.
	ul, err := c.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		c.Logger.Warn("seed resume: list addons", "err", err)
		return nil
	}
	var resumed []string
	for i := range ul.Items {
		var a kube.KusoAddon
		if derr := runtime.DefaultUnstructuredConverter.FromUnstructured(ul.Items[i].Object, &a); derr != nil {
			c.Logger.Warn("seed resume: decode addon", "name", ul.Items[i].GetName(), "err", derr)
			continue
		}
		sourceFQN := a.Annotations[seedPendingAnnotation]
		if sourceFQN == "" {
			continue
		}
		ns := a.Namespace
		project := a.Labels[kube.LabelProject]
		envScope := a.Labels[kube.LabelEnv]
		if project == "" || envScope == "" {
			c.Logger.Warn("seed resume: pending clone missing project/env label; skipping",
				"clone", a.Name, "ns", ns)
			continue
		}
		cloneFQN := a.Name
		// Completion check: if the latest seed Job for this clone already
		// succeeded, the seed landed and only the marker clear was lost.
		if c.latestSeedJobSucceeded(ctx, ns, project, cloneFQN) {
			c.clearSeedPending(ctx, ns, cloneFQN)
			c.Logger.Info("seed resume: seed already completed; cleared stale marker", "clone", cloneFQN)
			continue
		}
		if !c.tryAcquireSeed(cloneFQN) {
			continue
		}
		instancePG := a.Spec.UseInstanceAddon != ""
		seedCtx, cancel := context.WithTimeout(c.BaseCtx, 30*time.Minute)
		go func(ns, project, src, clone, scope string, ipg bool) {
			defer cancel()
			defer c.releaseSeed(clone)
			c.seedAsync(seedCtx, ns, project, src, clone, ipg, scope)
		}(ns, project, sourceFQN, cloneFQN, envScope, instancePG)
		resumed = append(resumed, cloneFQN)
		c.Logger.Info("seed resume: re-kicked interrupted clone seed",
			"clone", cloneFQN, "source", sourceFQN, "scope", envScope)
	}
	return resumed
}

// latestSeedJobSucceeded reports whether the most recent preview-seed Job
// for this clone finished successfully. False on any doubt (no typed
// client, list error, no Jobs, latest not succeeded) — the caller then
// re-seeds, which is the safe default for an incomplete clone.
func (c *Cloner) latestSeedJobSucceeded(ctx context.Context, ns, project, cloneFQN string) bool {
	if c.Kube.Clientset == nil {
		return false
	}
	jobs, err := c.Kube.Clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("kuso.sislelabs.com/role=preview-seed,kuso.sislelabs.com/clone-addon=%s", addons.ShortName(project, cloneFQN)),
	})
	if err != nil || len(jobs.Items) == 0 {
		return false
	}
	latest := jobs.Items[0]
	for i := range jobs.Items {
		if jobs.Items[i].CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = jobs.Items[i]
		}
	}
	return latest.Status.Succeeded > 0
}

// isPRClone reports whether addon a is a preview clone belonging to the
// PR identified by prLabel (the decimal PR number) / suffix ("-pr-<N>").
//
// Label-first: an addon carrying preview-pr=<N> is unambiguously this
// PR's clone. The name-suffix path is a legacy fallback for clones minted
// before the preview-pr label existed — but it's gated on the addon ALSO
// carrying the generic env label (kuso.sislelabs.com/env), so a real
// project addon that merely happens to be named "<x>-pr-<N>" (and has no
// preview/env labels at all) is never swept.
func isPRClone(a *kube.KusoAddon, prLabel, suffix string) bool {
	if a.Labels[previewPRLabel] == prLabel {
		return true
	}
	// Legacy fallback: pre-label clones. Require the generic env label so
	// we don't delete a non-preview addon that shares the -pr-N name shape.
	if a.Labels[kube.LabelEnv] != "" && strings.HasSuffix(a.Name, suffix) {
		return true
	}
	return false
}

// DeletePRAddons removes the postgres clones this project minted for PR
// <N>. Selection is by the kuso.sislelabs.com/preview-pr ownership label
// (with a legacy name-suffix fallback gated on the env label) — NEVER by
// the name suffix alone, or a real addon named e.g. "events-pr-2" would
// be dropped when PR #2 closes. Helm-operator's uninstall finalizer drops
// the StatefulSet + PVCs.
func (c *Cloner) DeletePRAddons(ctx context.Context, project string, prNumber int) error {
	if c == nil || c.Addons == nil {
		return nil
	}
	suffix := fmt.Sprintf("-pr-%d", prNumber)
	prLabel := fmt.Sprintf("%d", prNumber)
	all, err := c.Addons.List(ctx, project)
	if err != nil {
		return fmt.Errorf("list addons: %w", err)
	}
	var firstErr error
	for i := range all {
		a := &all[i]
		short := addons.ShortName(project, a.Name)
		if !isPRClone(a, prLabel, suffix) {
			continue
		}
		cloneFQN := a.Name
		if err := c.Addons.Delete(ctx, project, short); err != nil {
			c.Logger.Warn("preview db clone delete", "addon", short, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		// Reclaim the clone's StatefulSet VCT PVC. helm uninstall does NOT
		// delete it (VCT PVCs are never helm-owned, and carry
		// resource-policy=keep semantics), so without this every closed PR
		// leaks its clone volume — and because clone names are
		// deterministic (<base>-pr-N), the NEXT PR #N in the same repo
		// mounts the previous PR's database. The VCT PVC is named
		// data-<cloneFQN>-<ordinal>; a single-pod clone has ordinal 0, but
		// list-and-match the prefix to be robust. (HIGH-6a)
		c.reclaimClonePVCs(ctx, project, cloneFQN)
		c.Logger.Info("preview db clone deleted", "addon", short)
	}
	return firstErr
}

// reclaimClonePVCs force-deletes the StatefulSet VCT PVCs for a deleted
// preview clone. Best-effort: a still-mounted PVC gets a deletionTimestamp
// and GCs once the StatefulSet is gone.
func (c *Cloner) reclaimClonePVCs(ctx context.Context, project, cloneFQN string) {
	if c.Kube == nil || c.Kube.Clientset == nil {
		return
	}
	ns := c.namespaceFor(ctx, project)
	prefix := "data-" + cloneFQN + "-"
	pvcs, err := c.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		c.Logger.Warn("preview clone pvc reclaim: list", "clone", cloneFQN, "err", err)
		return
	}
	for i := range pvcs.Items {
		name := pvcs.Items[i].Name
		// Exact VCT match: "data-<cloneFQN>-<ordinal>". Guard against a
		// prefix collision with a longer clone name by requiring the tail
		// after the prefix to be all digits.
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if ord := name[len(prefix):]; ord == "" || strings.ContainsFunc(ord, func(r rune) bool { return r < '0' || r > '9' }) {
			continue
		}
		if derr := c.Kube.Clientset.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			c.Logger.Warn("preview clone pvc reclaim: delete", "pvc", name, "err", derr)
		}
	}
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
		// Honor cancellation (graceful shutdown / 30-min seedCtx timeout)
		// instead of blocking a bare 5s Sleep past a cancelled ctx.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
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
		c.clearSeedPending(ctx, ns, cloneFQN)
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
	c.clearSeedPending(ctx, ns, cloneFQN)
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
			// Surface the failure (skipping the migrate) so seedAsync does
			// NOT clear the seed-pending marker — the boot-time resume sweep
			// then re-checks this clone instead of treating it as seeded.
			return fmt.Errorf("seed job %s did not complete (migrate skipped): %w", waitName, werr)
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
