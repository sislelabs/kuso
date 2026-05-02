package kube

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
func list[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, namespace string, opts metav1.ListOptions) ([]T, error) {
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

// GetKusoAddon fetches one KusoAddon by name.
func (c *Client) GetKusoAddon(ctx context.Context, namespace, name string) (*KusoAddon, error) {
	return get[KusoAddon](ctx, c, GVRAddons, namespace, name)
}

// ListKusoBuilds returns all KusoBuild CRs in namespace.
func (c *Client) ListKusoBuilds(ctx context.Context, namespace string) ([]KusoBuild, error) {
	return list[KusoBuild](ctx, c, GVRBuilds, namespace, metav1.ListOptions{})
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
