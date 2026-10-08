package builds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

var sweepNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// fakeRegistry is a registry:2 v2 API over httptest: catalog + tag list
// (paged two per page to exercise Link handling), HEAD/GET manifests,
// config blobs and DELETE by digest.
type fakeRegistry struct {
	mu      sync.Mutex
	tags    map[string]map[string]string // repo → tag → digest
	created map[string]time.Time         // digest → image created
	deleted []string                     // "repo@digest"
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{tags: map[string]map[string]string{}, created: map[string]time.Time{}}
}

func (f *fakeRegistry) put(repo, tag, digest string, created time.Time) {
	if f.tags[repo] == nil {
		f.tags[repo] = map[string]string{}
	}
	f.tags[repo][tag] = digest
	f.created[digest] = created
}

func (f *fakeRegistry) page(w http.ResponseWriter, r *http.Request, key string, all []string) {
	sort.Strings(all)
	start := 0
	if last := r.URL.Query().Get("last"); last != "" {
		start = sort.SearchStrings(all, last) + 1
	}
	end := min(start+2, len(all))
	if end < len(all) {
		w.Header().Set("Link", fmt.Sprintf(`<%s?last=%s&n=2>; rel="next"`, r.URL.Path, all[end-1]))
	}
	_ = json.NewEncoder(w).Encode(map[string][]string{key: all[start:end]})
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v2/")
	switch {
	case p == "_catalog":
		var repos []string
		for repo := range f.tags {
			repos = append(repos, repo)
		}
		f.page(w, r, "repositories", repos)
	case strings.HasSuffix(p, "/tags/list"):
		repo := strings.TrimSuffix(p, "/tags/list")
		var tags []string
		for t := range f.tags[repo] {
			tags = append(tags, t)
		}
		f.page(w, r, "tags", tags)
	case strings.Contains(p, "/manifests/"):
		repo, ref, _ := strings.Cut(p, "/manifests/")
		if r.Method != http.MethodDelete && !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
			http.Error(w, "accept lacks OCI index", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodHead:
			dg, ok := f.tags[repo][ref]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", dg)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config":    map[string]string{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "cfg-" + ref},
			})
		case http.MethodDelete:
			f.deleted = append(f.deleted, repo+"@"+ref)
			for t, dg := range f.tags[repo] {
				if dg == ref {
					delete(f.tags[repo], t)
				}
			}
			w.WriteHeader(http.StatusAccepted)
		}
	case strings.Contains(p, "/blobs/cfg-"):
		_, dg, _ := strings.Cut(p, "/blobs/cfg-")
		_ = json.NewEncoder(w).Encode(map[string]time.Time{"created": f.created[dg]})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeRegistry) deletedSorted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.deleted)
	sort.Strings(out)
	return out
}

func startFakeRegistry(t *testing.T, f *fakeRegistry) RegistryInventory {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return newRegistryClient(strings.TrimPrefix(srv.URL, "http://"))
}

// fakeClusterKube builds a kube.Client whose dynamic + core fakes hold
// objs, spread over namespaces so the cluster-wide reads are exercised.
func fakeClusterKube(t *testing.T, seeds []seed, pods ...*corev1.Pod) (*kube.Client, *dynamicfake.FakeDynamicClient, *fake.Clientset) {
	t.Helper()
	cs := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed pod: %v", err)
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRBuilds:       "KusoBuildList",
		kube.GVRCrons:        "KusoCronList",
		kube.GVRRuns:         "KusoRunList",
	})
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s/%s: %v", s.gvr.Resource, s.obj.GetName(), err)
		}
	}
	return &kube.Client{Clientset: cs, Dynamic: dyn}, dyn, cs
}

func inNS(s seed, ns string) seed {
	s.obj.SetNamespace(ns)
	return s
}

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func sweepOn(t *testing.T, kc *kube.Client, reg RegistryInventory, mode OrphanSweepMode) OrphanSweepResult {
	t.Helper()
	res, err := SweepOrphanRepositories(context.Background(), kc, reg, mode, OrphanSweepGrace, sweepNow, quietLogger)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return res
}

var oldImage = sweepNow.Add(-30 * 24 * time.Hour)

func TestSweepOrphanRepositories_UntagsOrphansOnly(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry()
	f.put("live/web", "a1", "sha256:live1", oldImage)
	f.put("live/web", "buildcache", "sha256:livecache", oldImage)
	f.put("gone/api", "b1", "sha256:gone1", oldImage)
	f.put("gone/api", "b2", "sha256:gone2", oldImage)
	f.put("gone/api", "b2-retag", "sha256:gone2", oldImage) // shares b2's manifest
	f.put("live/old", "c1", "sha256:old1", oldImage)        // service deleted, project lives
	f.put("young/web", "d1", "sha256:young1", sweepNow.Add(-24*time.Hour))
	f.put("busy/web", "e1", "sha256:busy1", oldImage)
	f.put("library/nginx/extra", "x", "sha256:other", oldImage) // not kuso-shaped
	reg := startFakeRegistry(t, f)

	kc, _, _ := fakeClusterKube(t, []seed{
		inNS(seedProject("live", "main", "https://github.com/example/live", 0), "tenant-a"),
		inNS(seedService("live", "web"), "tenant-a"),
		seedBuild(&kube.KusoBuild{
			ObjectMeta: metav1.ObjectMeta{Name: "busy-web-1", Namespace: "kuso", Annotations: map[string]string{annPhase: "running"}},
			Spec:       kube.KusoBuildSpec{Project: "busy", Service: "busy-web"},
		}),
	})

	res := sweepOn(t, kc, reg, OrphanSweepOn)

	want := []string{"gone/api@sha256:gone1", "gone/api@sha256:gone2", "live/old@sha256:old1"}
	if got := f.deletedSorted(); !slices.Equal(got, want) {
		t.Fatalf("deleted %v, want %v", got, want)
	}
	if res.ReposScanned != 6 || res.ReposSwept != 2 || res.Manifests != 3 || res.ReposTooYoung != 1 || res.ReposBuilding != 1 {
		t.Errorf("unexpected summary: %+v", res)
	}
}

// TestSweepOrphanRepositories_KeepsReferencedImages: an orphan repo can
// still hold the image a surviving env, cron, run or pod runs (an env
// whose project CR was removed, a worker reusing a deleted service's
// image). Each reference type must keep its manifest, including tags
// that merely share the protected digest.
func TestSweepOrphanRepositories_KeepsReferencedImages(t *testing.T) {
	t.Parallel()
	const host = "kuso-registry.kuso.svc.cluster.local:5000/"
	f := newFakeRegistry()
	f.put("gone/api", "env", "sha256:env", oldImage)
	f.put("gone/api", "env-twin", "sha256:env", oldImage)
	f.put("gone/api", "pending", "sha256:pending", oldImage)
	f.put("gone/api", "cron", "sha256:cron", oldImage)
	f.put("gone/api", "run", "sha256:run", oldImage)
	f.put("gone/api", "pod-tag", "sha256:podtag", oldImage)
	f.put("gone/api", "pod-digest", "sha256:poddigest", oldImage)
	f.put("gone/api", "stale", "sha256:stale", oldImage)
	reg := startFakeRegistry(t, f)

	img := func(tag string) *kube.KusoImage { return &kube.KusoImage{Repository: host + "gone/api", Tag: tag} }
	kc, _, _ := fakeClusterKube(t, []seed{
		inNS(typedSeed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-production", Namespace: "tenant-b"},
			Spec:       kube.KusoEnvironmentSpec{Project: "other", Image: img("env"), PendingImage: img("pending")},
		}), "tenant-b"),
		typedSeed(kube.GVRCrons, "KusoCron", &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "kuso"},
			Spec:       kube.KusoCronSpec{Project: "other", Image: img("cron")},
		}),
		typedSeed(kube.GVRRuns, "KusoRun", &kube.KusoRun{
			ObjectMeta: metav1.ObjectMeta{Name: "once", Namespace: "kuso"},
			Spec:       kube.KusoRunSpec{Project: "other", Image: img("run")},
		}),
	},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "elsewhere"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: host + "gone/api:pod-tag"}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "kuso"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: host + "gone/api:moved-on"}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", ImageID: "docker-pullable://" + host + "gone/api@sha256:poddigest",
			}}},
		},
	)

	res := sweepOn(t, kc, reg, OrphanSweepOn)

	if got, want := f.deletedSorted(), []string{"gone/api@sha256:stale"}; !slices.Equal(got, want) {
		t.Fatalf("deleted %v, want only %v", got, want)
	}
	if res.ManifestsKept != 6 {
		t.Errorf("kept %d manifests, want 6: %+v", res.ManifestsKept, res)
	}
}

// TestSweepOrphanRepositories_ListErrorAborts: every protection and
// ownership read fails the sweep closed — nothing is deleted.
func TestSweepOrphanRepositories_ListErrorAborts(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"kusoprojects", "kusoservices", "kusoenvironments", "kusocrons", "kusoruns", "kusobuilds", "pods"} {
		t.Run(resource, func(t *testing.T) {
			t.Parallel()
			f := newFakeRegistry()
			f.put("gone/api", "b1", "sha256:gone1", oldImage)
			reg := startFakeRegistry(t, f)
			kc, dyn, cs := fakeClusterKube(t, nil)
			fail := func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, errors.New("apiserver unavailable")
			}
			if resource == "pods" {
				cs.PrependReactor("list", resource, fail)
			} else {
				dyn.PrependReactor("list", resource, fail)
			}

			_, err := SweepOrphanRepositories(context.Background(), kc, reg, OrphanSweepOn, OrphanSweepGrace, sweepNow, quietLogger)
			if err == nil {
				t.Fatal("expected the sweep to abort")
			}
			if got := f.deletedSorted(); len(got) != 0 {
				t.Errorf("deleted %v despite a failed %s list", got, resource)
			}
		})
	}
}

func TestSweepOrphanRepositories_DryRunDeletesNothing(t *testing.T) {
	t.Parallel()
	f := newFakeRegistry()
	f.put("gone/api", "b1", "sha256:gone1", oldImage)
	f.put("gone/api", "b2", "sha256:gone2", oldImage)
	reg := startFakeRegistry(t, f)
	kc, _, _ := fakeClusterKube(t, nil)

	res := sweepOn(t, kc, reg, OrphanSweepDryRun)

	if got := f.deletedSorted(); len(got) != 0 {
		t.Fatalf("dry-run deleted %v", got)
	}
	if res.Manifests != 2 || res.ReposOrphaned != 1 {
		t.Errorf("dry-run should report what it would delete: %+v", res)
	}
}

func TestParseOrphanSweepMode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]OrphanSweepMode{
		"": OrphanSweepDryRun, "dryrun": OrphanSweepDryRun, "yes": OrphanSweepDryRun,
		"on": OrphanSweepOn, " ON ": OrphanSweepOn, "off": OrphanSweepOff,
	} {
		if got := ParseOrphanSweepMode(in); got != want {
			t.Errorf("ParseOrphanSweepMode(%q) = %q, want %q", in, got, want)
		}
	}
}
