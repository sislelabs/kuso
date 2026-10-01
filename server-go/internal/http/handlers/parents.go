package handlers

import (
	"context"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// requireParent writes a 404 and returns false when project, or service
// within it (when service is non-empty), doesn't exist. List endpoints
// used to answer 200 [] for a typo'd parent, so `kuso build list shop wbe`
// read as "no builds" instead of an error. Other kube errors pass through
// (true): the list call that follows reports them. A nil client skips the
// check, for handlers wired without kube.
func requireParent(ctx context.Context, w http.ResponseWriter, kc *kube.Client, homeNS, project, service string) bool {
	if kc == nil || kc.Dynamic == nil {
		return true
	}
	if homeNS == "" {
		homeNS = "kuso"
	}
	p, err := lookupCR(ctx, kc, kube.GVRProjects, homeNS, project)
	if apierrors.IsNotFound(err) {
		writeErr(w, http.StatusNotFound, "project "+project+" not found")
		return false
	}
	if err != nil || service == "" {
		return true
	}
	ns := homeNS
	if v, _, _ := unstructured.NestedString(p.Object, "spec", "namespace"); v != "" {
		ns = v
	}
	short := strings.TrimPrefix(service, project+"-")
	if _, err := lookupCR(ctx, kc, kube.GVRServices, ns, project+"-"+short); apierrors.IsNotFound(err) {
		writeErr(w, http.StatusNotFound, "service "+project+"/"+short+" not found")
		return false
	}
	return true
}

// lookupCR reads from the informer cache when it's synced (these checks
// run on every poll of a list endpoint), else from the API server.
func lookupCR(ctx context.Context, kc *kube.Client, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	if kc.Cache != nil {
		if items, ok := kc.Cache.ListFromCache(gvr, ns, labels.Everything()); ok {
			for _, u := range items {
				if u.GetName() == name {
					return u, nil
				}
			}
			return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
		}
	}
	return kc.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}
