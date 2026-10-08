// Cancel + Rollback for the build pipeline. Extracted from builds.go
// in the v0.12 refactor pass alongside admission.go and cards.go.
// Create() remains in builds.go because the bulk of that file's
// remaining surface is the multi-step Create flow it sits at the
// centre of.
package builds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// Cancel marks an in-flight build as cancelled and tears down its
// kaniko Job. The CR itself is preserved (with phase=cancelled +
// build-state=done) so the deployments list still shows it in the
// history rather than a hole. Cancelling a finished build is a no-op
// 400 — the Job's already gone and the phase is fixed.
func (s *Service) Cancel(ctx context.Context, project, service, buildName string) error {
	if buildName == "" {
		return fmt.Errorf("%w: empty build name", ErrInvalid)
	}
	b, err := s.Kube.GetKusoBuild(ctx, s.nsFor(ctx, project), buildName)
	if apierrors.IsNotFound(err) || (err == nil && !buildOwnedBy(b, project, service)) {
		return fmt.Errorf("%w: build %s", ErrNotFound, buildName)
	}
	if err != nil {
		return fmt.Errorf("get build: %w", err)
	}
	return s.cancelBuild(ctx, project, buildName, "cancelled by user")
}

func envOwnedBy(e *kube.KusoEnvironment, project string) bool {
	if e.Spec.Project != "" {
		return e.Spec.Project == project
	}
	return e.Labels[kube.LabelProject] == project
}

// buildOwnedBy guards the user-facing build mutators: builds of all
// projects without a custom namespace share one namespace, so a name
// lookup alone reaches other projects' builds.
func buildOwnedBy(b *kube.KusoBuild, project, service string) bool {
	return b != nil && b.Spec.Project == project && b.Spec.Service == project+"-"+service
}

// cancelBuild is the shared cancel core behind the user-initiated
// Cancel, the webhook ref-deletion cleanup (CancelBuildsForRef), and the
// poller's clone-ref-missing diversion. `reason` is stored as the build
// message and shown in `kuso build list` / `build why`. All cancel paths
// emit build.cancelled at severity=info — never the @here build.failed —
// so a vanished ref never pages the on-call.
func (s *Service) cancelBuild(ctx context.Context, project, buildName, reason string) error {
	if buildName == "" {
		return fmt.Errorf("%w: empty build name", ErrInvalid)
	}
	ns := s.nsFor(ctx, project)
	b, err := s.Kube.GetKusoBuild(ctx, ns, buildName)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: build %s", ErrNotFound, buildName)
	}
	if err != nil {
		return fmt.Errorf("get build: %w", err)
	}
	phase := buildPhase(b)
	if phase == "succeeded" || phase == "failed" || phase == "cancelled" {
		return fmt.Errorf("%w: build %s already in phase %q", ErrInvalid, buildName, phase)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Stamp metadata AND blank spec.image.tag so the helm chart's
	// `if and .Values.image.tag ...` guard short-circuits — the
	// chart renders zero objects, no Job, no ServiceAccount, no
	// helm-managed children.
	//
	// Why this matters: cancel deletes the Job + helm release secrets
	// directly, but if the operator is offline at cancel time (or
	// restarts later), its initial cache sync ignores the watch
	// selector and reconciles every CR — re-installing the helm
	// release and re-creating the Job. Defanging the chart at the
	// values level is the only way to make cancel idempotent against
	// future operator catch-ups.
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:%q,%q:null},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true,"image":{"tag":""}}}`,
		annPhase, annCompletedAt, now, annMessage, reason,
		// Clear any promotion hold: a cancelled build must not render
		// as "held" nor count as awaiting-the-wave to siblings.
		annPromoteHold,
	)
	if _, perr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, buildName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
		return fmt.Errorf("patch build cancelled: %w", perr)
	}
	s.deleteCloneTokenSecret(ns, buildName)
	bg := metav1.DeletePropagationBackground
	if jerr := s.Kube.Clientset.BatchV1().Jobs(ns).Delete(ctx, buildName, metav1.DeleteOptions{
		PropagationPolicy: &bg,
	}); jerr != nil && !apierrors.IsNotFound(jerr) {
		// Don't fail the whole Cancel — the CR is already stamped, the
		// poller won't promote it. Worst case the kaniko Job runs for a
		// few more minutes producing an image nothing will use.
		slog.Default().Warn("builds: delete cancelled job", "err", jerr, "build", buildName)
	}
	// Wait briefly for the build pod to actually disappear so the
	// caller (and the deployments tab refetch) sees a clean state.
	awaitPodGone(ctx, s.Kube, ns, buildName, 5*time.Second)
	if s.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, project+"-")
		// b predates the patch; mirror the completion stamp so the card
		// gets its "Stopped after" duration.
		if b.Annotations == nil {
			b.Annotations = map[string]string{}
		}
		b.Annotations[annCompletedAt] = now
		targets := lookupBuildTargets(ctx, s.Kube, ns, s.Namespace, b)
		title, desc, fields := buildRichCard(b, short, "cancelled", reason, targets)
		s.Notifier.Emit(EventEnvelope{
			Type:        eventBuildCancelled,
			Title:       title,
			Description: desc,
			Project:     project,
			Service:     short,
			Env:         singleTargetEnv(targets),
			URL:         buildEventURL(project, short),
			Severity:    "info",
			DurationMs:  buildDurationMs(b),
			Fields:      fields,
			Links:       buildCardLinks(project, short, "cancelled", targets, nil),
		})
	}
	return nil
}

// CancelBuildsForRef cancels every in-flight (queued / pending / running)
// build in a project whose branch matches `branch`, transitioning them to
// cancelled with the given reason instead of letting them clone a vanished
// ref and fail. Called from the webhook dispatcher when a PR is
// closed/merged or a branch is deleted. Returns the number cancelled.
// Best-effort: a per-build cancel error is logged, not propagated, so one
// stuck build doesn't block cancelling the rest.
func (s *Service) CancelBuildsForRef(ctx context.Context, project, branch, reason string) (int, error) {
	if s.Kube == nil || branch == "" {
		return 0, nil
	}
	ns := s.nsFor(ctx, project)
	raw, err := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return 0, fmt.Errorf("list builds for ref cancel: %w", err)
	}
	n := 0
	for i := range raw {
		b := &raw[i]
		// Only in-flight builds — a `build-state=done` label means it
		// already reached a terminal phase (succeeded/failed/cancelled).
		if b.Labels["kuso.sislelabs.com/build-state"] == "done" {
			continue
		}
		if b.Spec.Branch != branch {
			continue
		}
		if cerr := s.cancelBuild(ctx, project, b.Name, reason); cerr != nil {
			// Already-terminal races are expected and benign.
			if errors.Is(cerr, ErrInvalid) {
				continue
			}
			slog.Default().Warn("builds: cancel for ref", "build", b.Name, "branch", branch, "err", cerr)
			continue
		}
		n++
	}
	return n, nil
}

// CancelPreviewBuilds cancels every in-flight build made for one of the
// named preview envs (AnnPreviewEnv). The PR-close path uses this instead
// of CancelBuildsForRef: a PR's head branch name says nothing about which
// builds belong to the PR (a fork PR from `mallory:main` shares its head
// name with production), but the preview env a build was made for does.
func (s *Service) CancelPreviewBuilds(ctx context.Context, project string, previewEnvs []string, reason string) (int, error) {
	if s.Kube == nil || len(previewEnvs) == 0 {
		return 0, nil
	}
	ns := s.nsFor(ctx, project)
	raw, err := s.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return 0, fmt.Errorf("list builds for preview cancel: %w", err)
	}
	n := 0
	for i := range raw {
		b := &raw[i]
		if b.Labels["kuso.sislelabs.com/build-state"] == "done" {
			continue
		}
		target := b.Annotations[AnnPreviewEnv]
		if target == "" || !slices.Contains(previewEnvs, target) {
			continue
		}
		if cerr := s.cancelBuild(ctx, project, b.Name, reason); cerr != nil {
			if errors.Is(cerr, ErrInvalid) {
				continue
			}
			slog.Default().Warn("builds: cancel preview build", "build", b.Name, "env", target, "err", cerr)
			continue
		}
		n++
	}
	return n, nil
}

// RollbackOptions tunes a Rollback. Force skips the branch check (a
// deliberate cross-branch rollback); Actor names who asked, for the
// notification card.
type RollbackOptions struct {
	Force bool
	Actor string
}

// Rollback re-points an env at a previous build's image tag. The
// build must be in phase=succeeded — rolling to a failed build would
// land a broken pod — and, unless opts.Force, built from the branch the
// env tracks: rolling a feature-branch image onto production is almost
// always a misclick. envName is the env-group label ("production",
// "staging", "preview-pr-7") or the CR name; empty means production.
// Returns the patched env.
func (s *Service) Rollback(ctx context.Context, project, service, envName, buildName string, opts RollbackOptions) (*kube.KusoEnvironment, error) {
	ns := s.nsFor(ctx, project)
	// Resolve the build's image. Prefer the live CR; if retention has
	// GC'd it, fall back to the archived BuildRecord (whose image may
	// still exist in the registry within imageRetentionWindow). Either
	// path yields (repo, tag) for a SUCCEEDED build, or an error.
	var imageRepo, imageTag, buildBranch string
	bRaw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, buildName, metav1.GetOptions{})
	switch {
	case err == nil:
		var b kube.KusoBuild
		if derr := runtime.DefaultUnstructuredConverter.FromUnstructured(bRaw.Object, &b); derr != nil {
			return nil, fmt.Errorf("decode build: %w", derr)
		}
		if !buildOwnedBy(&b, project, service) {
			return nil, fmt.Errorf("%w: build %s not found", ErrNotFound, buildName)
		}
		// A hold-expired build is cancelled but its image is complete;
		// its own message points users here to deploy it.
		holdExpired := buildPhase(&b) == "cancelled" && b.Annotations[annHoldExpired] != ""
		if buildPhase(&b) != "succeeded" && !holdExpired {
			return nil, fmt.Errorf("%w: build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", ErrInvalid, buildName, buildPhase(&b))
		}
		if b.Spec.Image == nil || b.Spec.Image.Tag == "" {
			return nil, fmt.Errorf("%w: build %s has no image to roll back to", ErrInvalid, buildName)
		}
		imageRepo, imageTag, buildBranch = b.Spec.Image.Repository, b.Spec.Image.Tag, b.Spec.Branch
	case apierrors.IsNotFound(err) && s.RecordLookup != nil:
		// CR gone — try the archive.
		repo, tag, phase, branch, ok, lerr := s.RecordLookup.GetBuildImage(ctx, project, buildName)
		if lerr != nil {
			return nil, fmt.Errorf("get build record: %w", lerr)
		}
		// The lookup is keyed by project only; the repo names the
		// service, so a sibling's archived build can't be rolled here.
		if !ok || repo != fmt.Sprintf("%s/%s/%s", RegistryHost, project, service) {
			return nil, fmt.Errorf("%w: build %s not found", ErrNotFound, buildName)
		}
		if phase != "succeeded" {
			return nil, fmt.Errorf("%w: build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", ErrInvalid, buildName, phase)
		}
		if tag == "" {
			return nil, fmt.Errorf("%w: build %s has no archived image to roll back to (image was pruned past the retention window)", ErrInvalid, buildName)
		}
		imageRepo, imageTag, buildBranch = repo, tag, branch
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("%w: build %s not found", ErrNotFound, buildName)
	default:
		return nil, fmt.Errorf("get build: %w", err)
	}
	// Only a handful of images per service survive the registry sweep,
	// and the window counts every branch, so an older production build
	// can be gone. Rolling back to it left the old pod running behind an
	// ImagePullBackOff: the rollback silently never happened.
	if s.Images != nil && strings.HasPrefix(imageRepo, RegistryHost+"/") {
		dg, rerr := s.Images.ResolveTagDigest(ctx, strings.TrimPrefix(imageRepo, RegistryHost+"/"), imageTag)
		if rerr == nil && dg == "" {
			return nil, fmt.Errorf("%w: build %s's image %s is no longer in the registry (pruned by retention) — rebuild that commit instead", ErrInvalid, buildName, imageTag)
		}
	}
	cur, err := s.resolveEnv(ctx, ns, project, service, envName)
	if err != nil {
		return nil, err
	}
	envCRName := cur.Name
	group := envGroupName(cur, project+"-"+service)
	if !opts.Force && buildBranch != "" {
		defaultBranch := s.defaultBranchOf(ctx, project, service)
		if !promotionBranchMatches(buildBranch, cur.Spec.Branch, defaultBranch) {
			return nil, fmt.Errorf("%w: build %s was built from branch %q but %s deploys %q — pass force to roll back across branches",
				ErrInvalid, buildName, buildBranch, group, envBranch(cur, defaultBranch))
		}
	}
	// Stamp promotedAt to *now* (not the build's createdAt) so a stray
	// concurrent auto-promote of an older build can't silently
	// overwrite the user's rollback decision — last-trigger-wins
	// would otherwise let a stale auto-promote shadow the manual
	// rollback if its build CR happened to have a newer createdAt.
	prevBuild := cur.Annotations[annPromotedBuild]
	now := time.Now().UTC().Format(time.RFC3339Nano)
	patch := fmt.Sprintf(
		`{"spec":{"image":{"repository":%q,"tag":%q,"pullPolicy":"IfNotPresent"}},"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		imageRepo, imageTag,
		annPromotedBuild, buildName,
		annPromotedAt, now,
	)
	if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, envCRName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch env %s: %w", envCRName, err)
	}
	envRaw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).Get(ctx, envCRName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("re-read env: %w", err)
	}
	var e kube.KusoEnvironment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(envRaw.Object, &e); err != nil {
		return nil, fmt.Errorf("decode rolled-back env %s: %w", envCRName, err)
	}
	// Crons inherit the production image; leaving them on the bad build
	// kept scheduled jobs running it until the next deploy.
	if isProductionEnv(cur) {
		img := kube.KusoImage{Repository: imageRepo, Tag: imageTag}
		if cerr := s.repointCrons(ctx, ns, project, service, img, slog.Default()); cerr != nil {
			slog.Default().Warn("rollback: repoint crons", "project", project, "service", service, "err", cerr)
		}
	}
	s.emitRolledBack(project, service, group, buildName, prevBuild, imageTag, opts)
	return &e, nil
}

func (s *Service) emitRolledBack(project, service, group, buildName, prevBuild, imageTag string, opts RollbackOptions) {
	if s.Notifier == nil {
		return
	}
	who := opts.Actor
	if who == "" {
		who = "someone"
	}
	desc := fmt.Sprintf("%s rolled %s back to build `%s` (image `%s`).", who, group, buildName, imageTag)
	if prevBuild != "" {
		desc += fmt.Sprintf("\nPreviously live: `%s`.", prevBuild)
	}
	if opts.Force {
		desc += "\nForced across branches."
	}
	targets := []buildTarget{{Env: group}}
	s.Notifier.Emit(EventEnvelope{
		Type:        eventDeployRolledBack,
		Title:       fmt.Sprintf("↩ Rolled back · %s / %s → %s", project, service, group),
		Description: desc,
		Body:        desc,
		Project:     project,
		Service:     service,
		Env:         group,
		URL:         withEnvParam(buildEventURL(project, service), targets),
		Severity:    "warn",
		Fields: []EnvelopeField{
			{Name: "Build", Value: "`" + buildName + "`", Inline: true},
			{Name: "By", Value: who, Inline: true},
		},
	})
}
