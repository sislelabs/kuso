package kube

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// ListProjectPods returns every kuso-project pod cluster-wide (pods
// carrying the project label). Served from the pod informer when warm;
// a live LIST only on a cold cache. The returned pods may be the
// informer's own objects: callers must not mutate them.
func (c *Client) ListProjectPods(ctx context.Context) ([]*corev1.Pod, error) {
	req, err := labels.NewRequirement(LabelProject, selection.Exists, nil)
	if err != nil {
		return nil, fmt.Errorf("kube: project pod selector: %w", err)
	}
	if c.Cache != nil {
		if pods, ok := c.Cache.ListPodsByLabel(labels.NewSelector().Add(*req)); ok {
			return pods, nil
		}
	}
	if c.Clientset == nil {
		return nil, fmt.Errorf("kube: clientset not wired")
	}
	list, err := c.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: LabelProject})
	if err != nil {
		return nil, fmt.Errorf("kube: list project pods: %w", err)
	}
	out := make([]*corev1.Pod, len(list.Items))
	for i := range list.Items {
		out[i] = &list.Items[i]
	}
	return out, nil
}
