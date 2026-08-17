// Cache wraps a client-go DynamicSharedInformerFactory over the kuso
// CRDs so List* read paths can serve from an in-process cache
// instead of round-tripping the API server on every request.
//
// Why bother:
//
//	At 100 projects × 5 services × 3 envs the read paths used to LIST
//	the full CR set on every request. With a few users on the
//	dashboard plus the github dispatcher's periodic scans plus the
//	build poller, that's hundreds of LISTs per minute against the
//	k3s API server — each one re-marshalling the full unstructured
//	payload through json. The cache reduces this to one WATCH per
//	GVR and O(1) lookups.
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
	"sort"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	coreinformers "k8s.io/client-go/informers"
	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
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

	// Pod informer (typed core/v1) — used by the nodes endpoint and
	// the build poller, both of which used to do cluster-wide
	// Pods("").List() per request. The watch keeps an in-process index
	// keyed by Spec.NodeName so per-node pod counts are O(1).
	podFactory  coreinformers.SharedInformerFactory
	podInformer cache.SharedIndexInformer
	podLister   corelisters.PodLister
	podSynced   cache.InformerSynced

	// Deployment informer — every populateLiveStatus call used to do a
	// live Get(deployment); for a 10-env project + open canvas that
	// was 10 round-trips per 5-second tick. Cluster-wide watch + a
	// namespaced lister covers it.
	depLister appslisters.DeploymentLister
	depSynced cache.InformerSynced

	// Job informer — the build poller (5s), runs poller (5s), cronwatch
	// (30s cluster-wide LIST) and release-hook poll all read Jobs on a
	// tick. Those were live apiserver calls scaling with project count;
	// one shared WATCH replaces all of them.
	jobLister batchlisters.JobLister
	jobSynced cache.InformerSynced

	// Secret informer — the dashboard enriches EVERY service with its
	// managed-secret KEY NAMES, which was one live Secret GET per
	// service per page load.
	//
	// SECURITY/MEMORY: a Transform strips Data (and StringData) off
	// every Secret before it enters the indexer, so this cache holds
	// key NAMES only. Secret VALUES are never resident. Any caller that
	// needs a value must still go to the live API — see SecretKeysOnly.
	secretLister corelisters.SecretLister
	secretSynced cache.InformerSynced

	// Node informer — nodewatch.Watcher (30s tick) and nodemetrics.Sampler
	// (5min tick) used to Nodes().List() every iteration. On a 50-node
	// cluster that's ~500ms of apiserver work per tick. The informer
	// keeps an in-process view backed by one cluster-wide WATCH; the
	// ticker still drives the work cadence, but the data comes from
	// the indexer instead of a fresh LIST.
	nodeLister corelisters.NodeLister
	nodeSynced cache.InformerSynced

	mu      sync.RWMutex
	stopCh  chan struct{}
	stopped bool
}

// podByNodeIndexKey is the cache.Indexer key for Pod-by-node lookups.
const podByNodeIndexKey = "spec.nodeName"

type informerEntry struct {
	lister cache.GenericLister
	synced cache.InformerSynced
}

// NewCache builds a shared informer factory over every kuso CRD and
// starts all watches. The returned Cache is safe to use immediately —
// reads against an unsynced informer fall back to the live API.
func NewCache(c *Client) *Cache {
	if c == nil || c.Dynamic == nil {
		return nil
	}
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.Dynamic, resyncPeriod, "", nil)

	// Every kuso CRD belongs here. GVRRuns was missing for a long time
	// and it was the single largest source of apiserver load in the
	// control plane: list[T] silently falls through to a live LIST for
	// any uncached GVR, and the runs poller walks EVERY project
	// namespace on a 5s tick. At 100 projects that was ~1,200 live
	// LISTs/min for a resource type that is idle almost always.
	// If you add a CRD, add it here too.
	gvrs := []schema.GroupVersionResource{
		GVRProjects, GVRServices, GVREnvironments,
		GVRAddons, GVRBuilds, GVRCrons, GVRRuns,
	}
	informers := make(map[schema.GroupVersionResource]informerEntry, len(gvrs))
	for _, gvr := range gvrs {
		gi := factory.ForResource(gvr)
		informers[gvr] = informerEntry{
			lister: gi.Lister(),
			synced: gi.Informer().HasSynced,
		}
	}

	cc := &Cache{
		factory:   factory,
		informers: informers,
		stopCh:    make(chan struct{}),
	}

	// Pod informer — typed clientset, cluster-wide, indexed by node.
	// Without this every /api/kubernetes/nodes call and every build-
	// admission/cancel did a cluster-wide LIST (5–20 MB at 1k+ pods).
	if c.Clientset != nil {
		pf := coreinformers.NewSharedInformerFactory(c.Clientset, resyncPeriod)
		pi := pf.Core().V1().Pods()
		informer := pi.Informer()
		_ = informer.AddIndexers(cache.Indexers{
			podByNodeIndexKey: func(obj any) ([]string, error) {
				p, ok := obj.(*corev1.Pod)
				if !ok || p.Spec.NodeName == "" {
					return nil, nil
				}
				return []string{p.Spec.NodeName}, nil
			},
		})
		cc.podFactory = pf
		cc.podInformer = informer
		cc.podLister = pi.Lister()
		cc.podSynced = informer.HasSynced

		di := pf.Apps().V1().Deployments()
		cc.depLister = di.Lister()
		cc.depSynced = di.Informer().HasSynced

		// Node informer rides the same factory — Start() below kicks
		// every informer the factory knows about, so adding this here
		// is enough to bring it up. The lister returns typed
		// *corev1.Node objects so consumers (nodewatch / nodemetrics)
		// don't have to unmarshal unstructured.
		ni := pf.Core().V1().Nodes()
		cc.nodeLister = ni.Lister()
		cc.nodeSynced = ni.Informer().HasSynced

		ji := pf.Batch().V1().Jobs()
		cc.jobLister = ji.Lister()
		cc.jobSynced = ji.Informer().HasSynced

		// Secrets: strip the payload BEFORE it is indexed. The only
		// cached consumer needs key names, and keeping every Secret's
		// Data resident in every replica would be both a large memory
		// cost and an unnecessary blast-radius increase.
		si := pf.Core().V1().Secrets()
		_ = si.Informer().SetTransform(stripSecretData)
		cc.secretLister = si.Lister()
		cc.secretSynced = si.Informer().HasSynced
	}

	return cc
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
	if c.podFactory != nil {
		c.podFactory.Start(c.stopCh)
	}
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

// CRDInformer returns the SharedIndexInformer for one of kuso's CRD
// GVRs so a downstream caller can attach an AddEventHandler. Used by
// the build-reaper that watches KusoBuild transitions to done=true
// and deletes the matching helm-release secret so the operator's
// next reconcile can't resurrect the Job. Returns nil when the
// GVR isn't tracked (defensive — callers shouldn't see this in
// practice).
func (c *Cache) CRDInformer(gvr schema.GroupVersionResource) cache.SharedIndexInformer {
	if c == nil {
		return nil
	}
	return c.factory.ForResource(gvr).Informer()
}

// AllSynced returns true once every informer has finished its initial
// list. Cheap to call repeatedly — backs the readiness probe so the
// kube-LB doesn't send traffic before the cache is warm.
func (c *Cache) AllSynced() bool {
	if c == nil {
		return true
	}
	for _, e := range c.informers {
		if !e.synced() {
			return false
		}
	}
	if c.podSynced != nil && !c.podSynced() {
		return false
	}
	if c.depSynced != nil && !c.depSynced() {
		return false
	}
	if c.nodeSynced != nil && !c.nodeSynced() {
		return false
	}
	return true
}

// ListNodes returns a snapshot of every Node currently in the
// informer's local indexer. Returns (nil, false) when the cache
// isn't ready — callers fall back to a live Nodes().List(). The
// returned slice is owned by the caller; the informer's pointers
// are shared (same `*corev1.Node` instance every reader sees), so
// callers must NOT mutate the returned objects. Read-only is the
// only safe mode.
func (c *Cache) ListNodes() ([]*corev1.Node, bool) {
	if c == nil || c.nodeLister == nil || c.nodeSynced == nil || !c.nodeSynced() {
		return nil, false
	}
	nodes, err := c.nodeLister.List(labels.Everything())
	if err != nil {
		return nil, false
	}
	return nodes, true
}

// PodCountsByNode returns a node→pod-count map served from the local
// Pod informer index. Returns (nil, false) if the informer isn't ready
// yet — caller should fall back to a live LIST.
func (c *Cache) PodCountsByNode() (map[string]int, bool) {
	if c == nil || c.podInformer == nil || c.podSynced == nil || !c.podSynced() {
		return nil, false
	}
	out := map[string]int{}
	for _, key := range c.podInformer.GetIndexer().ListIndexFuncValues(podByNodeIndexKey) {
		objs, err := c.podInformer.GetIndexer().ByIndex(podByNodeIndexKey, key)
		if err != nil {
			continue
		}
		out[key] = len(objs)
	}
	return out, true
}

// GetDeployment returns the Deployment by namespace+name from the
// shared informer. Returns (nil, false) when the cache isn't ready
// or the deployment isn't found. Callers should fall back to a live
// Get when (nil, false) is returned and they actually need the
// object.
func (c *Cache) GetDeployment(namespace, name string) (*appsv1.Deployment, bool) {
	if c == nil || c.depLister == nil || c.depSynced == nil || !c.depSynced() {
		return nil, false
	}
	d, err := c.depLister.Deployments(namespace).Get(name)
	if err != nil || d == nil {
		return nil, false
	}
	return d, true
}

// GetPod returns one Pod by namespace/name from the Pod informer.
// Returns (nil, false) when the cache isn't ready or the pod isn't
// present — the caller decides whether to fall back to a live Get.
//
// Exists for the log-stream phase watcher, which probes a single pod's
// status on a 2s ticker for as long as a viewer has the log panel open.
// Against the live API that is one apiserver GET per pod per viewer
// every 2 seconds (50 viewers × 10 pods = 250 GET/s just to render a
// status string). The informer already watches Pods cluster-wide for
// ListPodsByLabel, so serving this from cache costs nothing extra.
func (c *Cache) GetPod(namespace, name string) (*corev1.Pod, bool) {
	if c == nil || c.podLister == nil || c.podSynced == nil || !c.podSynced() {
		return nil, false
	}
	p, err := c.podLister.Pods(namespace).Get(name)
	if err != nil || p == nil {
		return nil, false
	}
	return p, true
}

// ListPodsByLabel returns every Pod (cluster-wide) whose labels match
// the given selector, served from the Pod informer. Returns
// (nil, false) when the cache isn't ready — caller should fall back to
// a live LIST.
func (c *Cache) ListPodsByLabel(sel labels.Selector) ([]*corev1.Pod, bool) {
	if c == nil || c.podLister == nil || c.podSynced == nil || !c.podSynced() {
		return nil, false
	}
	if sel == nil {
		sel = labels.Everything()
	}
	pods, err := c.podLister.List(sel)
	if err != nil {
		return nil, false
	}
	return pods, true
}

// WaitForSync blocks until every informer has done its initial list,
// or ctx is canceled. Use only on boot if you really need a warm
// cache before serving — the read path doesn't require it.
func (c *Cache) WaitForSync(ctx context.Context) bool {
	if c == nil {
		return true
	}
	syncs := make([]cache.InformerSynced, 0, len(c.informers)+2)
	for _, e := range c.informers {
		syncs = append(syncs, e.synced)
	}
	if c.podSynced != nil {
		syncs = append(syncs, c.podSynced)
	}
	if c.jobSynced != nil {
		syncs = append(syncs, c.jobSynced)
	}
	if c.secretSynced != nil {
		syncs = append(syncs, c.secretSynced)
	}
	if c.depSynced != nil {
		syncs = append(syncs, c.depSynced)
	}
	return cache.WaitForCacheSync(ctx.Done(), syncs...)
}

// ListFromCache returns the cached items for gvr in namespace, or
// (nil, false) if the cache isn't ready for that GVR yet.
//
// "Not ready" means the informer hasn't completed its initial list —
// in which case the caller should fall back to a live API list so
// the user doesn't see a transient empty page on server boot.
//
// sel is applied client-side over the in-memory index. Pass
// labels.Everything() for unfiltered. Filtering against the cache is
// fundamentally cheaper than a live LIST because the indexer is
// already fully resident; this is the same model the upstream
// SharedInformer uses for its native typed listers.
func (c *Cache) ListFromCache(gvr schema.GroupVersionResource, namespace string, sel labels.Selector) ([]*unstructured.Unstructured, bool) {
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
	if sel == nil {
		sel = labels.Everything()
	}
	var (
		objs []runtime.Object
		err  error
	)
	if namespace == "" {
		objs, err = entry.lister.List(sel)
	} else {
		objs, err = entry.lister.ByNamespace(namespace).List(sel)
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

// stripSecretData is the informer Transform for Secrets. It drops the
// payload so the shared cache holds metadata + KEY NAMES only.
//
// Why: the sole cached consumer (managed-secret key enrichment on the
// dashboard) needs to know WHICH keys exist, never their values.
// Keeping values would put every Secret in the cluster — addon
// passwords, clone tokens, JWT signing keys — permanently in every
// replica's heap, for no benefit. It would also mean a heap dump or a
// memory-disclosure bug leaks everything at once.
//
// The keys are preserved by replacing each value with nil: `len(Data)`
// and `range Data` still work, so the lister is a drop-in for the key
// listing path, and any caller that wants a VALUE gets an empty byte
// slice rather than a wrong one — a bug that fails loudly.
func stripSecretData(obj any) (any, error) {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		// Tombstones and non-Secret objects pass through untouched.
		return obj, nil
	}
	if sec.Data == nil && sec.StringData == nil {
		return sec, nil
	}
	out := sec.DeepCopy()
	for k := range out.Data {
		out.Data[k] = nil
	}
	out.StringData = nil
	return out, nil
}

// GetJob returns a Job from the informer cache. Returns (nil, false)
// when the cache isn't ready or the Job isn't present — the caller must
// fall back to a live Get, because "absent from cache" is NOT the same
// as "does not exist" (a Job created moments ago may not have landed).
func (c *Cache) GetJob(namespace, name string) (*batchv1.Job, bool) {
	if c == nil || c.jobLister == nil || c.jobSynced == nil || !c.jobSynced() {
		return nil, false
	}
	job, err := c.jobLister.Jobs(namespace).Get(name)
	if err != nil || job == nil {
		return nil, false
	}
	return job, true
}

// ListJobs returns Jobs in a namespace (empty namespace = cluster-wide)
// matching sel, from the informer cache. (nil, false) when not ready.
func (c *Cache) ListJobs(namespace string, sel labels.Selector) ([]*batchv1.Job, bool) {
	if c == nil || c.jobLister == nil || c.jobSynced == nil || !c.jobSynced() {
		return nil, false
	}
	if sel == nil {
		sel = labels.Everything()
	}
	var (
		jobs []*batchv1.Job
		err  error
	)
	if namespace == "" {
		jobs, err = c.jobLister.List(sel)
	} else {
		jobs, err = c.jobLister.Jobs(namespace).List(sel)
	}
	if err != nil {
		return nil, false
	}
	return jobs, true
}

// SecretKeysOnly returns the KEY NAMES of a Secret from the informer
// cache, sorted. (nil, false) when the cache isn't ready or the Secret
// isn't present.
//
// Values are deliberately unavailable through this path — the cache
// stores none (see stripSecretData). Callers needing a value must use
// the live client.
func (c *Cache) SecretKeysOnly(namespace, name string) ([]string, bool) {
	if c == nil || c.secretLister == nil || c.secretSynced == nil || !c.secretSynced() {
		return nil, false
	}
	sec, err := c.secretLister.Secrets(namespace).Get(name)
	if err != nil || sec == nil {
		return nil, false
	}
	keys := make([]string, 0, len(sec.Data))
	for k := range sec.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, true
}
