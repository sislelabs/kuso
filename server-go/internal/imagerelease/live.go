package imagerelease

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// AnnLiveImages records, per env, the images that reached a fully ready
// rollout, so the release history can tell a rollback target that served
// traffic from one that never came up (a tag that never pulled, a crash on
// start). Since marks when recording began: releases older than it are
// simply unknown, not "never ran".
const AnnLiveImages = "kuso.sislelabs.com/live-images"

// maxLiveImages bounds the annotation; the oldest refs drop off.
const maxLiveImages = 30

type liveImages struct {
	Since string   `json:"since"`
	Refs  []string `json:"refs"`
}

func parseLiveImages(v string) liveImages {
	var l liveImages
	if v != "" {
		_ = json.Unmarshal([]byte(v), &l)
	}
	return l
}

// rolledOut: every desired replica runs the current template and is ready,
// and the template runs ref.
func rolledOut(d *appsv1.Deployment, ref string) bool {
	if d == nil || d.Spec.Replicas == nil || *d.Spec.Replicas == 0 {
		return false
	}
	if d.Status.ObservedGeneration < d.Generation {
		return false
	}
	want := *d.Spec.Replicas
	if d.Status.UpdatedReplicas < want || d.Status.ReadyReplicas < want || d.Status.Replicas != d.Status.UpdatedReplicas {
		return false
	}
	return slices.ContainsFunc(d.Spec.Template.Spec.Containers, func(c corev1.Container) bool { return c.Image == ref })
}

// recordLiveImage adds the env's current image to AnnLiveImages once its
// Deployment has fully rolled out. One GET per env whose image isn't
// recorded yet; nothing for the rest.
func (w *Watcher) recordLiveImage(ctx context.Context, ns string, e *kube.KusoEnvironment) {
	if e.Spec.Image == nil || e.Spec.Image.Repository == "" || e.Spec.Kind == "preview" {
		return
	}
	tag := e.Spec.Image.Tag
	if tag == "" {
		tag = "latest"
	}
	ref := e.Spec.Image.Repository + ":" + tag
	cur := parseLiveImages(e.Annotations[AnnLiveImages])
	if slices.Contains(cur.Refs, ref) {
		return
	}
	var dep *appsv1.Deployment
	if d, ok := w.Kube.Cache.GetDeployment(ns, e.Name); ok {
		dep = d
	} else if w.Kube.Clientset != nil {
		d, err := w.Kube.Clientset.AppsV1().Deployments(ns).Get(ctx, e.Name, metav1.GetOptions{})
		if err != nil {
			return
		}
		dep = d
	}
	if !rolledOut(dep, ref) {
		return
	}
	if err := addLiveImage(ctx, w.Kube, ns, e.Name, ref, time.Now()); err != nil {
		w.Logger.Warn("imagerelease: record live image", "env", e.Name, "image", ref, "err", err)
	}
}

// FieldManagerAnnotations owns kuso-server's bookkeeping annotations on
// env CRs. Drift reads the "kuso-server" managedFields time as the last
// spec edit; this write lands right after a rollout finishes, and under
// that name it re-armed the reconcile-pending grace, so the service said
// "Applying changes…" for two minutes after it was already live.
const FieldManagerAnnotations = "kuso-server-annotations"

// addLiveImage appends ref to the env's AnnLiveImages with a
// resourceVersion-guarded merge patch, retrying on conflict.
func addLiveImage(ctx context.Context, kc *kube.Client, ns, name, ref string, now time.Time) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var env *kube.KusoEnvironment
		env, err = kc.GetKusoEnvironment(ctx, ns, name)
		if err != nil {
			return err
		}
		l := parseLiveImages(env.Annotations[AnnLiveImages])
		if slices.Contains(l.Refs, ref) {
			return nil
		}
		if l.Since == "" {
			l.Since = now.UTC().Format(time.RFC3339)
		}
		l.Refs = append(l.Refs, ref)
		if len(l.Refs) > maxLiveImages {
			l.Refs = l.Refs[len(l.Refs)-maxLiveImages:]
		}
		b, merr := json.Marshal(l)
		if merr != nil {
			return merr
		}
		patch, merr := json.Marshal(map[string]any{"metadata": map[string]any{
			"resourceVersion": env.ResourceVersion,
			"annotations":     map[string]string{AnnLiveImages: string(b)},
		}})
		if merr != nil {
			return merr
		}
		_, err = kc.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManagerAnnotations})
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return err
}
