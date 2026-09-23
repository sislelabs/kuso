package reconcilehealth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

const testRegistry = "reg.local:5000"

func toUnstructured(t *testing.T, kind string, obj any) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: kube.GroupName, Version: kube.Version, Kind: kind})
	return u
}

func meta(name, ns, project string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"kuso.sislelabs.com/project": project}}
}

func fixtureEnv(name, ns, project, tag string, status map[string]any) *kube.KusoEnvironment {
	return &kube.KusoEnvironment{
		ObjectMeta: meta(name, ns, project),
		Spec: kube.KusoEnvironmentSpec{
			Project: project, Service: project + "-web",
			Image: &kube.KusoImage{Repository: testRegistry + "/" + project + "/web", Tag: tag},
		},
		Status: status,
	}
}

// scanFixture is a two-namespace cluster covering every scan branch that
// does not need a backup schedule: a failed addon release, a failed env
// release, missing registry images, live/orphan/platform conn Secrets, and
// a payload-heavy helm release Secret that must never influence the result.
func scanFixture(t *testing.T) (*dynamicfake.FakeDynamicClient, *fake.Clientset) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	})
	failed := cond("ReleaseFailed", "True", "upgrade failed: timed out")
	seeds := []struct {
		gvr schema.GroupVersionResource
		obj *unstructured.Unstructured
	}{
		{kube.GVRProjects, toUnstructured(t, "KusoProject", &kube.KusoProject{ObjectMeta: meta("alpha", "kuso", "alpha")})},
		{kube.GVRProjects, toUnstructured(t, "KusoProject", &kube.KusoProject{ObjectMeta: meta("beta", "kuso", "beta"), Spec: kube.KusoProjectSpec{Namespace: "kuso-beta"}})},
		{kube.GVRAddons, toUnstructured(t, "KusoAddon", &kube.KusoAddon{ObjectMeta: meta("alpha-db", "kuso", "alpha"), Status: failed})},
		{kube.GVRAddons, toUnstructured(t, "KusoAddon", &kube.KusoAddon{ObjectMeta: meta("alpha-cache", "kuso", "alpha")})},
		{kube.GVRAddons, toUnstructured(t, "KusoAddon", &kube.KusoAddon{ObjectMeta: meta("beta-db", "kuso-beta", "beta")})},
		{kube.GVREnvironments, toUnstructured(t, "KusoEnvironment", fixtureEnv("alpha-web-production", "kuso", "alpha", "gone", nil))},
		{kube.GVREnvironments, toUnstructured(t, "KusoEnvironment", fixtureEnv("alpha-web-staging", "kuso", "alpha", "live", nil))},
		{kube.GVREnvironments, toUnstructured(t, "KusoEnvironment", fixtureEnv("alpha-worker-production", "kuso", "alpha", "live", failed))},
		{kube.GVREnvironments, toUnstructured(t, "KusoEnvironment", fixtureEnv("beta-web-production", "kuso-beta", "beta", "gone", nil))},
	}
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", s.obj.GetName(), err)
		}
	}
	sec := func(name, ns, project string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: meta(name, ns, project), Data: map[string][]byte{"PASSWORD": []byte("x")}}
	}
	helmRelease := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.alpha-db.v1", Namespace: "kuso", Labels: map[string]string{"owner": "helm", "name": "alpha-db"}},
		Type:       "helm.sh/release.v1",
		Data:       map[string][]byte{"release": make([]byte, 64<<10)},
	}
	cs := fake.NewSimpleClientset(
		sec("alpha-db-conn", "kuso", "alpha"),
		sec("alpha-old-conn", "kuso", "alpha"),
		sec("kuso-postgres-conn", "kuso", ""),
		sec("alpha-web-secrets", "kuso", "alpha"),
		helmRelease,
		sec("beta-db-conn", "kuso-beta", "beta"),
		sec("beta-gone-conn", "kuso-beta", "beta"),
	)
	return dyn, cs
}

// countingProber is a fakeProber that also records how many HEADs run at once.
type countingProber struct {
	fakeProber
	delay    time.Duration
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	calls    atomic.Int32
}

func (p *countingProber) ResolveTagDigest(ctx context.Context, repo, tag string) (string, error) {
	p.calls.Add(1)
	n := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	for {
		m := p.maxSeen.Load()
		if n <= m || p.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(p.delay)
	return p.fakeProber.ResolveTagDigest(ctx, repo, tag)
}

func newProber() *countingProber {
	return &countingProber{fakeProber: fakeProber{present: map[string]bool{
		"alpha/web:live": true,
		"beta/web:live":  true,
	}}}
}

type issueKey struct {
	Resource string
	Kind     Kind
	Severity Severity
}

// The worked-out expected report for scanFixture, independent of the code.
var wantFixtureIssues = []issueKey{
	{"alpha-web-production", KindImageMissingFromRegistry, SeverityCritical},
	{"alpha-worker-production", KindReleaseFailed, SeverityCritical},
	{"beta-web-production", KindImageMissingFromRegistry, SeverityCritical},
	{"alpha-db", KindReleaseFailed, SeverityWarning},
	{"alpha-old-conn", KindOrphanConnSecret, SeverityWarning},
	{"beta-gone-conn", KindOrphanConnSecret, SeverityWarning},
}

func checkFixtureReport(t *testing.T, rep *Report) {
	t.Helper()
	got := make([]issueKey, len(rep.Issues))
	for i, iss := range rep.Issues {
		got[i] = issueKey{iss.Resource, iss.Kind, iss.Severity}
	}
	if !reflect.DeepEqual(got, wantFixtureIssues) {
		t.Errorf("issues:\n got  %v\n want %v", got, wantFixtureIssues)
	}
	if rep.Scanned != 7 || rep.Healthy != 3 || rep.Critical != 3 || rep.Warning != 3 || rep.Info != 0 || len(rep.SkippedNamespaces) != 0 {
		t.Errorf("counts: scanned=%d healthy=%d critical=%d warning=%d info=%d skipped=%v; want 7/3/3/3/0/none",
			rep.Scanned, rep.Healthy, rep.Critical, rep.Warning, rep.Info, rep.SkippedNamespaces)
	}
}

// Before the cache: no informer, every read is a live list.
func TestScan_LivePathReport(t *testing.T) {
	dyn, cs := scanFixture(t)
	s := &Scanner{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Images: newProber(), RegistryHost: testRegistry}
	rep, err := s.Scan(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureReport(t, rep)
}

// With a warm informer the orphan sweep must read Secret names from the
// cache, not LIST every Secret payload from the apiserver, and the report
// must be identical to the live-path one.
func TestScan_CachedPathMatchesLiveWithoutListingSecrets(t *testing.T) {
	dyn, cs := scanFixture(t)
	live := &Scanner{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Images: newProber(), RegistryHost: testRegistry}
	want, err := live.Scan(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}

	dyn2, cs2 := scanFixture(t)
	kc := &kube.Client{Dynamic: dyn2, Clientset: cs2}
	kc.Cache = kube.NewCache(kc)
	kc.Cache.Start()
	t.Cleanup(kc.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !kc.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}
	cs2.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("live Secret LIST issued — expected informer-cache read")
	})

	cached := &Scanner{Kube: kc, Images: newProber(), RegistryHost: testRegistry}
	got, err := cached.Scan(ctx, "kuso")
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureReport(t, got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cached report differs from live report:\n got  %+v\n want %+v", got, want)
	}
}

// One serial registry HEAD per env made the scan O(envs × RTT).
func TestScan_ProbesImagesConcurrently(t *testing.T) {
	dyn, cs := scanFixture(t)
	p := newProber()
	p.delay = 50 * time.Millisecond
	s := &Scanner{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Images: p, RegistryHost: testRegistry}
	rep, err := s.Scan(context.Background(), "kuso")
	if err != nil {
		t.Fatal(err)
	}
	checkFixtureReport(t, rep)
	if p.maxSeen.Load() < 2 {
		t.Errorf("max concurrent registry HEADs = %d, want >= 2", p.maxSeen.Load())
	}
}

// Every open Health tab polls every 30s; repeated polls inside the TTL
// must reuse one scan, and a failed scan must not be cached.
func TestCachedScanner_ReusesReportWithinTTL(t *testing.T) {
	dyn, cs := scanFixture(t)
	p := newProber()
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	c := &CachedScanner{
		Scanner: &Scanner{Kube: &kube.Client{Dynamic: dyn, Clientset: cs}, Images: p, RegistryHost: testRegistry},
		TTL:     30 * time.Second,
		Now:     func() time.Time { mu.Lock(); defer mu.Unlock(); return now },
	}
	ctx := context.Background()
	scans := func() int32 { return p.calls.Load() / 3 } // 3 envs reach the registry probe per scan

	for i := 0; i < 3; i++ {
		rep, err := c.Scan(ctx, "kuso")
		if err != nil {
			t.Fatal(err)
		}
		checkFixtureReport(t, rep)
	}
	if scans() != 1 {
		t.Fatalf("scans within TTL = %d, want 1", scans())
	}

	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	if _, err := c.Scan(ctx, "kuso"); err != nil {
		t.Fatal(err)
	}
	if scans() != 2 {
		t.Fatalf("scans after TTL expiry = %d, want 2", scans())
	}

	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	dyn.PrependReactor("list", kube.GVRAddons.Resource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("apiserver down")
	})
	if _, err := c.Scan(ctx, "kuso"); err == nil {
		t.Fatal("want error from failed scan")
	}
	if _, err := c.Scan(ctx, "kuso"); err == nil {
		t.Fatal("a failed scan was cached: second call returned no error")
	}
}
