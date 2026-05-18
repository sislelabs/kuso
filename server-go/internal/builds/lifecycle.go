// Cancel + Rollback for the build pipeline. Extracted from builds.go
// in the v0.12 refactor pass alongside admission.go and cards.go.
// Create() remains in builds.go because the bulk of that file's
// remaining surface is the multi-step Create flow it sits at the
// centre of.
package builds

import (
	"context"
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
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:"cancelled by user"},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true,"image":{"tag":""}}}`,
		annPhase, annCompletedAt, now, annMessage,
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

// Rollback re-points the production env at a previous build's image
// tag. The build must be in phase=succeeded — rolling to a failed
// build would land a broken pod. Returns the patched env.
func (s *Service) Rollback(ctx context.Context, project, service, buildName string) (*kube.KusoEnvironment, error) {
	ns := s.nsFor(ctx, project)
	bRaw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).Get(ctx, buildName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get build: %w", err)
	}
	var b kube.KusoBuild
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(bRaw.Object, &b); err != nil {
		return nil, fmt.Errorf("decode build: %w", err)
	}
	if buildPhase(&b) != "succeeded" {
		return nil, fmt.Errorf("build %s is in phase %q, not succeeded — refuse to roll back to a non-succeeded build", buildName, buildPhase(&b))
	}
	if b.Spec.Image == nil {
		return nil, fmt.Errorf("build %s has no image to roll back to", buildName)
	}
	// Patch the production env's image to the build's image. Stamp
	// promotedAt to *now* (not the build's createdAt) so a stray
	// concurrent auto-promote of an older build can't silently
	// overwrite the user's rollback decision — last-trigger-wins
	// would otherwise let a stale auto-promote shadow the manual
	// rollback if its build CR happened to have a newer createdAt.
	envName := project + "-" + service + "-production"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	patch := fmt.Sprintf(
		`{"spec":{"image":{"repository":%q,"tag":%q,"pullPolicy":"IfNotPresent"}},"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		b.Spec.Image.Repository, b.Spec.Image.Tag,
		annPromotedBuild, b.Name,
		annPromotedAt, now,
	)
	if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
		Patch(ctx, envName, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("patch env %s: %w", envName, err)
	}
	envRaw, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("re-read env: %w", err)
	}
	var e kube.KusoEnvironment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(envRaw.Object, &e); err != nil {
		return nil, fmt.Errorf("decode rolled-back env %s: %w", envName, err)
	}
	return &e, nil
}
