// Cache wraps a client-go DynamicSharedInformerFactory over the six
// kuso CRDs so List* read paths can serve from an in-process cache
// instead of round-tripping the API server on every request.
//
// Why bother:
//
//   At 100 projects × 5 services × 3 envs the read paths used to LIST
//   the full CR set on every request. With a few users on the
//   dashboard plus the github dispatcher's periodic scans plus the
//   build poller, that's hundreds of LISTs per minute against the
//   k3s API server — each one re-marshalling the full unstructured
//   payload through json. The cache reduces this to one WATCH per
//   GVR and O(1) lookups. See SCALABILITY_ANALYSIS.md §3.
//
// Design notes:
//
//   - We watch cluster-scoped (Namespace("")) for every GVR. kuso is
//     multi-namespace but never has more than ~10 namespaces in
//     practice; serving every list by filtering the cluster-wide
//     cache by namespace is cheaper than running N namespaced
//     informers.
//   - Writes still go through the dynamic client directly. The
//     informer's watch picks up the write and updates the cache;
//     callers who immediately List after a Create may briefly miss
//     the new object. Acceptable — the same caller already has the
//     created object in hand.
//   - If the cache hasn't synced yet (fresh server boot), List
//     transparently falls back to the live dynamic client. No
//     "wait for sync" blocking on the request path.
//   - Get goes straight to the live API. Get-by-name is rare on
//     hot paths and the get-then-update pattern needs current
//     resourceVersion anyway.
package kube

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

// resyncPeriod is how often the informer reconciles its cache against
// the API server even when no watch events are received. 10 minutes
// is the upstream default and is safe — watches are the primary
// update path; resync is the safety net for missed events.
const resyncPeriod = 10 * time.Minute

// Cache holds shared informers over kuso CRDs.
type Cache struct {
	factory dynamicinformer.DynamicSharedInformerFactory

	// One informer per GVR. We track them so we can poll their HasSynced
	// state cheaply on the read path.
	informers map[schema.GroupVersionResource]informerEntry

	mu       sync.RWMutex
	stopCh   chan struct{}
	stopped  bool
}

type informerEntry struct {
	lister cache.GenericLister
	synced cache.InformerSynced
}

// NewCache builds a shared informer factory over kuso's six CRDs and
// starts all watches. The returned Cache is safe to use immediately —
// reads against an unsynced informer fall back to the live API.
func NewCache(c *Client) *Cache {
	if c == nil || c.Dynamic == nil {
		return nil
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.Dynamic, resyncPeriod, "", nil)

	gvrs := []schema.GroupVersionResource{
		GVRProjects, GVRServices, GVREnvironments,
		GVRAddons, GVRBuilds, GVRCrons,
	}
	informers := make(map[schema.GroupVersionResource]informerEntry, len(gvrs))
	for _, gvr := range gvrs {
		gi := factory.ForResource(gvr)
		informers[gvr] = informerEntry{
			lister: gi.Lister(),
			synced: gi.Informer().HasSynced,
		}
	}

	return &Cache{
		factory:   factory,
		informers: informers,
		stopCh:    make(chan struct{}),
	}
}

// Start kicks off the watches. Idempotent.
func (c *Cache) Start() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	c.factory.Start(c.stopCh)
}

// Stop halts the watches. Used in tests + on graceful shutdown.
func (c *Cache) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	close(c.stopCh)
	c.stopped = true
}

// WaitForSync blocks until every informer has done its initial list,
// or ctx is canceled. Use only on boot if you really need a warm
// cache before serving — the read path doesn't require it.
func (c *Cache) WaitForSync(ctx context.Context) bool {
	if c == nil {
		return true
	}
	syncs := make([]cache.InformerSynced, 0, len(c.informers))
	for _, e := range c.informers {
		syncs = append(syncs, e.synced)
	}
	return cache.WaitForCacheSync(ctx.Done(), syncs...)
}

// listFromCache returns the cached items for gvr in namespace, or
// (nil, false) if the cache isn't ready for that GVR yet.
//
// "Not ready" means the informer hasn't completed its initial list —
// in which case the caller should fall back to a live API list so
// the user doesn't see a transient empty page on server boot.
func (c *Cache) listFromCache(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, bool) {
	if c == nil {
		return nil, false
	}
	entry, ok := c.informers[gvr]
	if !ok {
		return nil, false
	}
	if !entry.synced() {
		return nil, false
	}
	var (
		objs []runtime.Object
		err  error
	)
	if namespace == "" {
		objs, err = entry.lister.List(labels.Everything())
	} else {
		objs, err = entry.lister.ByNamespace(namespace).List(labels.Everything())
	}
	if err != nil {
		return nil, false
	}
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		// The lister hands back pointers into its cache. Callers
		// decode through fromUnstructured into typed structs, which
		// copies — but a defensive DeepCopy here would protect any
		// future caller that mutates. Cheap belt-and-braces.
		out = append(out, u.DeepCopy())
	}
	return out, true
}

