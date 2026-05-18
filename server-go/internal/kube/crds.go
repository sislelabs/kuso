package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
)


// fromUnstructured decodes a single unstructured object into out.
//
// runtime.DefaultUnstructuredConverter handles json struct tags correctly,
// so the typed structs in types.go round-trip without any manual mapping.
func fromUnstructured(u *unstructured.Unstructured, out any) error {
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out); err != nil {
		return fmt.Errorf("kube: decode %s/%s: %w", u.GetKind(), u.GetName(), err)
	}
	return nil
}

func decodeList[T any](list *unstructured.UnstructuredList, gvr schema.GroupVersionResource) ([]T, error) {
	out := make([]T, 0, len(list.Items))
	for i := range list.Items {
		var item T
		if err := fromUnstructured(&list.Items[i], &item); err != nil {
			return nil, fmt.Errorf("kube: decode %s item %d: %w", gvr.Resource, i, err)
		}
		out = append(out, item)
	}
	return out, nil
}

// list is the generic dynamic-client → typed-slice helper.
//
// When c.Cache is set and synced for gvr the list is served from the
// local informer cache. LabelSelector is parsed and applied
// client-side against the in-memory index, which is far cheaper than
// a live LIST — the indexer is already fully resident, the selector
// is just a filter pass over a Go slice. FieldSelector still goes to
// the live API because field indices vary by resource version.
func list[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace string, opts metav1.ListOptions) ([]T, error) {
	if c.Cache != nil && opts.FieldSelector == "" {
		sel := labels.Everything()
		if opts.LabelSelector != "" {
			parsed, err := labels.Parse(opts.LabelSelector)
			if err == nil {
				sel = parsed
			} else {
				// Bad selector — bail to the live API which will
				// surface the parse error to the caller.
				sel = nil
			}
		}
		if sel != nil {
			if items, ok := c.Cache.ListFromCache(gvr, namespace, sel); ok {
				out := make([]T, 0, len(items))
				for _, u := range items {
					var item T
					if err := fromUnstructured(u, &item); err != nil {
						return nil, fmt.Errorf("kube: decode cached %s: %w", gvr.Resource, err)
					}
					out = append(out, item)
				}
				return out, nil
			}
		}
	}
	raw, err := c.Dynamic.Resource(gvr).Namespace(namespace).List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("kube: list %s in %q: %w", gvr.Resource, namespace, err)
	}
	return decodeList[T](raw, gvr)
}

// get is the generic dynamic-client → typed helper.
func get[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace, name string) (*T, error) {
	raw, err := c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: get %s/%s in %q: %w", gvr.Resource, name, namespace, err)
	}
	var out T
	if err := fromUnstructured(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ---- Per-kind public API. Phase 1 ships read-only — Get + List. Writes
// land in Phase 3 onward, where each call site needs the right verb.

// ListKusoProjects returns all KusoProject CRs in namespace.
func (c *Client) ListKusoProjects(ctx context.Context, namespace string) ([]KusoProject, error) {
	return list[KusoProject](ctx, c, GVRProjects, namespace, metav1.ListOptions{})
}

// GetKusoProject fetches one KusoProject by name.
func (c *Client) GetKusoProject(ctx context.Context, namespace, name string) (*KusoProject, error) {
	return get[KusoProject](ctx, c, GVRProjects, namespace, name)
}

// ListKusoServices returns all KusoService CRs in namespace.
func (c *Client) ListKusoServices(ctx context.Context, namespace string) ([]KusoService, error) {
	return list[KusoService](ctx, c, GVRServices, namespace, metav1.ListOptions{})
}

// GetKusoService fetches one KusoService by name.
func (c *Client) GetKusoService(ctx context.Context, namespace, name string) (*KusoService, error) {
	return get[KusoService](ctx, c, GVRServices, namespace, name)
}

// ListKusoEnvironments returns all KusoEnvironment CRs in namespace.
func (c *Client) ListKusoEnvironments(ctx context.Context, namespace string) ([]KusoEnvironment, error) {
	return list[KusoEnvironment](ctx, c, GVREnvironments, namespace, metav1.ListOptions{})
}

// GetKusoEnvironment fetches one KusoEnvironment by name.
func (c *Client) GetKusoEnvironment(ctx context.Context, namespace, name string) (*KusoEnvironment, error) {
	return get[KusoEnvironment](ctx, c, GVREnvironments, namespace, name)
}

// ListKusoAddons returns all KusoAddon CRs in namespace.
func (c *Client) ListKusoAddons(ctx context.Context, namespace string) ([]KusoAddon, error) {
	return list[KusoAddon](ctx, c, GVRAddons, namespace, metav1.ListOptions{})
}

// ListKusoAddonsByLabels returns KusoAddon CRs in namespace matching
// the supplied label pairs. The selector is encoded through
// kube.LabelSelector so user-controlled values can't reshape the
// selector at the apiserver. Routes through the cached typed-list
// helper, so when the informer is warm this is a slice filter, not
// a network call.
func (c *Client) ListKusoAddonsByLabels(ctx context.Context, namespace string, labels map[string]string) ([]KusoAddon, error) {
	return list[KusoAddon](ctx, c, GVRAddons, namespace, metav1.ListOptions{
		LabelSelector: LabelSelector(labels),
	})
}

// GetKusoAddon fetches one KusoAddon by name.
func (c *Client) GetKusoAddon(ctx context.Context, namespace, name string) (*KusoAddon, error) {
	return get[KusoAddon](ctx, c, GVRAddons, namespace, name)
}

// ListKusoEnvironmentsByLabels returns KusoEnvironment CRs in
// namespace matching the supplied label pairs. Routes through the
// cached typed-list helper — warm informer = slice filter, cold =
// network call. Used by every "list envs for a service" hot path:
// propagateChangedToEnvs, drift detection, env-rename, pod
// enumeration, the build poller. Before this helper existed, all
// those sites went through Dynamic.Resource(GVREnvironments).List
// directly and bypassed the cache (pass-4 P1-1).
func (c *Client) ListKusoEnvironmentsByLabels(ctx context.Context, namespace string, labels map[string]string) ([]KusoEnvironment, error) {
	return list[KusoEnvironment](ctx, c, GVREnvironments, namespace, metav1.ListOptions{
		LabelSelector: LabelSelector(labels),
	})
}

// ListKusoBuilds returns all KusoBuild CRs in namespace.
func (c *Client) ListKusoBuilds(ctx context.Context, namespace string) ([]KusoBuild, error) {
	return list[KusoBuild](ctx, c, GVRBuilds, namespace, metav1.ListOptions{})
}

// ListKusoBuildsByLabels returns KusoBuild CRs in namespace matching
// the supplied label pairs. Cache-friendly; used by the build
// poller's tick (was previously per-namespace Dynamic.Resource list,
// bypassing the cache) and the find-recent-by-branch lookup.
func (c *Client) ListKusoBuildsByLabels(ctx context.Context, namespace string, labels map[string]string) ([]KusoBuild, error) {
	return list[KusoBuild](ctx, c, GVRBuilds, namespace, metav1.ListOptions{
		LabelSelector: LabelSelector(labels),
	})
}

// GetKusoBuild fetches one KusoBuild by name.
func (c *Client) GetKusoBuild(ctx context.Context, namespace, name string) (*KusoBuild, error) {
	return get[KusoBuild](ctx, c, GVRBuilds, namespace, name)
}

// GetKuso fetches the singleton Kuso config CR by name (typically "kuso").
//
// The TS server reads this on boot to seed runpacks/podsizes. The spec is
// preserve-unknown-fields, so callers index into Spec[...] directly.
func (c *Client) GetKuso(ctx context.Context, namespace, name string) (*Kuso, error) {
	return get[Kuso](ctx, c, GVRKuso, namespace, name)
}

// ListKusoes lists all Kuso config CRs in namespace. Should usually return
// at most one item, but we list rather than get-by-fixed-name so the
// caller can decide what to do when the cluster is bare.
func (c *Client) ListKusoes(ctx context.Context, namespace string) ([]Kuso, error) {
	return list[Kuso](ctx, c, GVRKuso, namespace, metav1.ListOptions{})
}

// ---- Write ops -----------------------------------------------------------

// toUnstructured serialises a typed CRD struct into the unstructured shape
// the dynamic client requires, attaching the proper apiVersion + kind.
func toUnstructured(obj any, gvr schema.GroupVersionResource, kind string) (*unstructured.Unstructured, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("kube: encode %s: %w", kind, err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: gvr.Group, Version: gvr.Version, Kind: kind})
	return u, nil
}

// create is the generic typed → dynamic create helper.
func create[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, kind, namespace string, obj *T) (*T, error) {
	u, err := toUnstructured(obj, gvr, kind)
	if err != nil {
		return nil, err
	}
	created, err := c.Dynamic.Resource(gvr).Namespace(namespace).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("kube: create %s in %q: %w", gvr.Resource, namespace, err)
	}
	var out T
	if err := fromUnstructured(created, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// update is the generic typed → dynamic update helper. Uses Update
// (PUT) and bumps resourceVersion to whatever the apiserver currently
// has on conflict, then retries. helm-operator continuously patches
// .status; without retry, a Spec write that arrives during a status
// reconcile silently 409s and the caller sees a generic 500. The
// caller's mutation is already baked into `obj` so we can't re-run
// it — instead we bump rv and retry the same shape, which is correct
// for spec-only edits because helm-operator only writes status.
func update[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, kind, namespace string, obj *T) (*T, error) {
	u, err := toUnstructured(obj, gvr, kind)
	if err != nil {
		return nil, err
	}
	var updated *unstructured.Unstructured
	rerr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		var uerr error
		updated, uerr = c.Dynamic.Resource(gvr).Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{})
		if uerr == nil {
			return nil
		}
		// On conflict, pick up the live resourceVersion and retry.
		latest, gerr := c.Dynamic.Resource(gvr).Namespace(namespace).Get(ctx, u.GetName(), metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		u.SetResourceVersion(latest.GetResourceVersion())
		return uerr
	})
	if rerr != nil {
		return nil, fmt.Errorf("kube: update %s in %q: %w", gvr.Resource, namespace, rerr)
	}
	var out T
	if err := fromUnstructured(updated, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func deleteCR(ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace, name string) error {
	if err := c.Dynamic.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("kube: delete %s/%s in %q: %w", gvr.Resource, name, namespace, err)
	}
	return nil
}

// ErrAbortRetry signals updateWithRetry to bail out cleanly without
// writing — for the "the desired state is already present" / "the
// mutation discovered a conflict on its own terms" path. The caller
// sees a nil object back and decides what HTTP status to map.
var ErrAbortRetry = fmt.Errorf("kube: abort retry")

// updateWithRetry runs `mutate` against a freshly-fetched copy of the
// CR and retries on conflict. helm-operator continuously patches
// .status; without retry, a Spec write that arrives during a status
// reconcile silently 409s. The mutate callback re-runs against the
// fresh object on every retry, so multi-step ops (read latest, check
// invariants, mutate) get correct read-modify-write semantics across
// kube optimistic concurrency.
//
// Returns ErrAbortRetry when the callback explicitly aborts (no write
// performed, error wrapped). Anything else goes through standard
// RetryOnConflict.
func updateWithRetry[T any](
	ctx context.Context,
	c *Client,
	gvr schema.GroupVersionResource,
	kind, namespace, name string,
	mutate func(*T) error,
) (*T, error) {
	var out T
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest, gerr := get[T](ctx, c, gvr, namespace, name)
		if gerr != nil {
			return gerr
		}
		if merr := mutate(latest); merr != nil {
			// Don't retry — the callback decided this op is done.
			return merr
		}
		u, terr := toUnstructured(latest, gvr, kind)
		if terr != nil {
			return terr
		}
		updated, uerr := c.Dynamic.Resource(gvr).Namespace(namespace).Update(ctx, u, metav1.UpdateOptions{})
		if uerr != nil {
			return uerr
		}
		return fromUnstructured(updated, &out)
	})
	if err != nil {
		return nil, fmt.Errorf("kube: update with retry %s/%s in %q: %w", gvr.Resource, name, namespace, err)
	}
	return &out, nil
}

// UpdateKusoServiceWithRetry is the read-modify-write entry point for
// concurrent service-spec edits. mutate runs against the freshly-
// fetched CR and is re-run on every conflict, so a stale-rv check
// inside the callback survives mid-flight helm-operator status
// patches.
func (c *Client) UpdateKusoServiceWithRetry(ctx context.Context, namespace, name string, mutate func(*KusoService) error) (*KusoService, error) {
	return updateWithRetry[KusoService](ctx, c, GVRServices, "KusoService", namespace, name, mutate)
}

// UpdateKusoEnvironmentWithRetry — RMW variant for env CRs. Used by
// the propagation path (propagateChangedToEnvs) so a helm-operator
// status patch landing mid-loop doesn't overwrite our new spec with
// the stale snapshot the caller assembled before the loop started.
// Without this, a service-spec change that fans out to N envs has
// an N% chance of losing one or more env writes whenever the
// operator is reconciling at the same time.
func (c *Client) UpdateKusoEnvironmentWithRetry(ctx context.Context, namespace, name string, mutate func(*KusoEnvironment) error) (*KusoEnvironment, error) {
	return updateWithRetry[KusoEnvironment](ctx, c, GVREnvironments, "KusoEnvironment", namespace, name, mutate)
}

// UpdateKusoAddonWithRetry — same shape for addon CRs. Addon settings
// edits (placement, size, version) race against the operator's
// helm-release status patches; without RMW retry, a Settings → Save
// from the UI that lands during a reconcile silently reverts.
func (c *Client) UpdateKusoAddonWithRetry(ctx context.Context, namespace, name string, mutate func(*KusoAddon) error) (*KusoAddon, error) {
	return updateWithRetry[KusoAddon](ctx, c, GVRAddons, "KusoAddon", namespace, name, mutate)
}

// UpdateKusoCronWithRetry — same shape for cron CRs. Cron schedule /
// command edits race against the operator's status patches.
func (c *Client) UpdateKusoCronWithRetry(ctx context.Context, namespace, name string, mutate func(*KusoCron) error) (*KusoCron, error) {
	return updateWithRetry[KusoCron](ctx, c, GVRCrons, "KusoCron", namespace, name, mutate)
}

// CreateKusoProject creates a new KusoProject CR.
func (c *Client) CreateKusoProject(ctx context.Context, namespace string, p *KusoProject) (*KusoProject, error) {
	return create[KusoProject](ctx, c, GVRProjects, "KusoProject", namespace, p)
}

// UpdateKusoProject replaces an existing KusoProject's spec.
func (c *Client) UpdateKusoProject(ctx context.Context, namespace string, p *KusoProject) (*KusoProject, error) {
	return update[KusoProject](ctx, c, GVRProjects, "KusoProject", namespace, p)
}

// DeleteKusoProject deletes a KusoProject by name.
func (c *Client) DeleteKusoProject(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRProjects, namespace, name)
}

// CreateKusoService creates a new KusoService CR.
func (c *Client) CreateKusoService(ctx context.Context, namespace string, s *KusoService) (*KusoService, error) {
	return create[KusoService](ctx, c, GVRServices, "KusoService", namespace, s)
}

// UpdateKusoService replaces an existing KusoService's spec.
func (c *Client) UpdateKusoService(ctx context.Context, namespace string, s *KusoService) (*KusoService, error) {
	return update[KusoService](ctx, c, GVRServices, "KusoService", namespace, s)
}

// DeleteKusoService deletes a KusoService by name.
func (c *Client) DeleteKusoService(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRServices, namespace, name)
}

// DeleteKusoEnvironment deletes a KusoEnvironment by name. Used by
// preview-cleanup and the explicit DELETE endpoint.
func (c *Client) DeleteKusoEnvironment(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVREnvironments, namespace, name)
}

// CreateKusoEnvironment creates a new KusoEnvironment CR. Used by the
// preview-env flow.
func (c *Client) CreateKusoEnvironment(ctx context.Context, namespace string, e *KusoEnvironment) (*KusoEnvironment, error) {
	return create[KusoEnvironment](ctx, c, GVREnvironments, "KusoEnvironment", namespace, e)
}

// CreateKusoBuild creates a new KusoBuild CR.
func (c *Client) CreateKusoBuild(ctx context.Context, namespace string, b *KusoBuild) (*KusoBuild, error) {
	return create[KusoBuild](ctx, c, GVRBuilds, "KusoBuild", namespace, b)
}

// DeleteKusoBuild removes a KusoBuild by name.
func (c *Client) DeleteKusoBuild(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRBuilds, namespace, name)
}

// CreateKusoAddon creates a new KusoAddon CR. Used by the env-group
// clone flow when the user picks "fresh" for an addon — the existing
// addons.Service.Add path goes through HTTP-handler validation that
// over-restricts the allowed kinds; here we just need to mirror an
// already-validated production addon's spec into a new env's namespace.
func (c *Client) CreateKusoAddon(ctx context.Context, namespace string, a *KusoAddon) (*KusoAddon, error) {
	return create[KusoAddon](ctx, c, GVRAddons, "KusoAddon", namespace, a)
}

// UpdateKusoAddon replaces an existing KusoAddon's spec. Used by the
// addon settings flow (placement edits, future size/version updates).
func (c *Client) UpdateKusoAddon(ctx context.Context, namespace string, a *KusoAddon) (*KusoAddon, error) {
	return update[KusoAddon](ctx, c, GVRAddons, "KusoAddon", namespace, a)
}

// DeleteKusoAddon removes the CR. Helm-operator's release uninstall
// cascades the StatefulSet, the data PVC, and the <addon>-conn Secret.
func (c *Client) DeleteKusoAddon(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRAddons, namespace, name)
}

// ---- KusoCron CRUD --------------------------------------------------

func (c *Client) ListKusoCrons(ctx context.Context, namespace string) ([]KusoCron, error) {
	return list[KusoCron](ctx, c, GVRCrons, namespace, metav1.ListOptions{})
}

func (c *Client) GetKusoCron(ctx context.Context, namespace, name string) (*KusoCron, error) {
	return get[KusoCron](ctx, c, GVRCrons, namespace, name)
}

func (c *Client) CreateKusoCron(ctx context.Context, namespace string, k *KusoCron) (*KusoCron, error) {
	return create[KusoCron](ctx, c, GVRCrons, "KusoCron", namespace, k)
}

func (c *Client) UpdateKusoCron(ctx context.Context, namespace string, k *KusoCron) (*KusoCron, error) {
	return update[KusoCron](ctx, c, GVRCrons, "KusoCron", namespace, k)
}

func (c *Client) DeleteKusoCron(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRCrons, namespace, name)
}

// ---- KusoRun CRUD ---------------------------------------------------
//
// One-shot task pods. Same shape as the other CR wrappers; the
// run-phase + run-completedAt lifecycle annotations are read by the
// runs domain service from .metadata.annotations on the live CR.

func (c *Client) ListKusoRuns(ctx context.Context, namespace string) ([]KusoRun, error) {
	return list[KusoRun](ctx, c, GVRRuns, namespace, metav1.ListOptions{})
}

func (c *Client) GetKusoRun(ctx context.Context, namespace, name string) (*KusoRun, error) {
	return get[KusoRun](ctx, c, GVRRuns, namespace, name)
}

func (c *Client) CreateKusoRun(ctx context.Context, namespace string, k *KusoRun) (*KusoRun, error) {
	return create[KusoRun](ctx, c, GVRRuns, "KusoRun", namespace, k)
}

func (c *Client) DeleteKusoRun(ctx context.Context, namespace, name string) error {
	return deleteCR(ctx, c, GVRRuns, namespace, name)
}

// UpdateKusoEnvironment replaces an existing KusoEnvironment's spec.
//
// NOTE: callers that mutate envFromSecrets values must also bump
// spec.secretsRev (§6.2) — this wrapper does not do that automatically.
// Use the secrets package, which has the right call ordering.
func (c *Client) UpdateKusoEnvironment(ctx context.Context, namespace string, e *KusoEnvironment) (*KusoEnvironment, error) {
	return update[KusoEnvironment](ctx, c, GVREnvironments, "KusoEnvironment", namespace, e)
}

// OwnerRefForService returns a metav1.OwnerReference pointing at the
// given KusoService — meant to be attached to child env/cron CRs so
// kube's GC reaps them automatically when the service is deleted.
//
// Why this matters: without an owner ref, deleting a service relies on
// our application-level cascade (DeleteService walks envs + crons +
// per-env secrets). That cascade can be skipped or partial — for
// example when an operator restart races with a DELETE request and
// part of the cascade fails, the children leak. With ownerReferences,
// kube's GC handles the cascade in-cluster on the next reconcile pass
// regardless of what the server-go side does. The application-level
// cascade still runs (it does the helm release cleanup the kube GC
// doesn't), but it's now belt-and-suspenders rather than the only
// safety net.
//
// `BlockOwnerDeletion=true` makes kube refuse to release the service
// finalizer until the child is gone — keeps the deletion ordering
// sane. `Controller=true` so only one ref per child claims control.
func OwnerRefForService(s *KusoService) metav1.OwnerReference {
	tru := true
	return metav1.OwnerReference{
		APIVersion:         GroupName + "/" + Version,
		Kind:               "KusoService",
		Name:               s.Name,
		UID:                s.UID,
		Controller:         &tru,
		BlockOwnerDeletion: &tru,
	}
}
