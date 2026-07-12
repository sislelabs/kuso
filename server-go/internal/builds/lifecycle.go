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
	return s.cancelBuild(ctx, project, buildName, "cancelled by user")
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
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:%q},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true,"image":{"tag":""}}}`,
		annPhase, annCompletedAt, now, annMessage, reason,
	)
	if _, perr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, buildName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); perr != nil {
		return fmt.Errorf("patch build cancelled: %w", perr)
	}
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
		title, desc, fields := buildRichCard(b, short, "cancelled", "", "")
		s.Notifier.Emit(EventEnvelope{
			Type:        eventBuildCancelled,
			Title:       title,
			Description: desc,
			Project:     project,
			Service:     short,
			URL:         buildEventURL(project, short),
			Severity:    "info",
			DurationMs:  buildDurationMs(b),
			Fields:      fields,
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

// Rollback re-points an env at a previous build's image tag. The
// build must be in phase=succeeded — rolling to a failed build would
// land a broken pod. envName is the env short name (e.g. "production",
// "staging"); empty defaults to "production" for backward compat with
// pre-v0.17.1 callers. Returns the patched env.
//
// Pre-v0.17.1 the envName was hardcoded to "production" — rolling
// back staging was either impossible (no UI path) OR if the caller
// passed a staging build name it silently patched production with
// staging code. The handler now passes the env from the URL so
// rolling back staging affects staging only.
func (s *Service) Rollback(ctx context.Context, project, service, envName, buildName string) (*kube.KusoEnvironment, error) {
	if envName == "" {
		envName = "production"
	}
	ns := s.nsFor(ctx, project)
	// Resolve the build's image. Prefer the live CR; if retention has
	// GC'd it, fall back to the archived BuildRecord (whose image may
	// still exist in the registry within imageRetentionWindow). Either
	// path yields (repo, tag) for a SUCCEEDED build, or an error.
	var imageRepo, imageTag string
	bRaw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, buildName, metav1.GetOptions{})
	switch {
	case err == nil:
		var b kube.KusoBuild
		if derr := runtime.DefaultUnstructuredConverter.FromUnstructured(bRaw.Object, &b); derr != nil {
			return nil, fmt.Errorf("decode build: %w", derr)
		}
		if buildPhase(&b) != "succeeded" {
			return nil, fmt.Errorf("build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", buildName, buildPhase(&b))
		}
		if b.Spec.Image == nil {
			return nil, fmt.Errorf("build %s has no image to roll back to", buildName)
		}
		imageRepo, imageTag = b.Spec.Image.Repository, b.Spec.Image.Tag
	case apierrors.IsNotFound(err) && s.RecordLookup != nil:
		// CR gone — try the archive.
		repo, tag, phase, ok, lerr := s.RecordLookup.GetBuildImage(ctx, project, buildName)
		if lerr != nil {
			return nil, fmt.Errorf("get build record: %w", lerr)
		}
		if !ok {
			return nil, fmt.Errorf("%w: build %s not found", ErrNotFound, buildName)
		}
		if phase != "succeeded" {
			return nil, fmt.Errorf("build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", buildName, phase)
		}
		if tag == "" {
			return nil, fmt.Errorf("build %s has no archived image to roll back to (image was pruned past the retention window)", buildName)
		}
		imageRepo, imageTag = repo, tag
	default:
		return nil, fmt.Errorf("get build: %w", err)
	}
	// Patch the addressed env's image to the build's image. Stamp
	// promotedAt to *now* (not the build's createdAt) so a stray
	// concurrent auto-promote of an older build can't silently
	// overwrite the user's rollback decision — last-trigger-wins
	// would otherwise let a stale auto-promote shadow the manual
	// rollback if its build CR happened to have a newer createdAt.
	envCRName := project + "-" + service + "-" + envName
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
	return &e, nil
}
