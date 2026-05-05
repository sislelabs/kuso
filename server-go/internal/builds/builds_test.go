package builds

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func fakeService(t *testing.T, seeds ...seed) *Service {
	t.Helper()
	cs := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRBuilds:       "KusoBuildList",
	})
	for _, s := range seeds {
		if err := dyn.Tracker().Create(s.gvr, s.obj, "kuso"); err != nil {
			t.Fatalf("seed %s/%s: %v", s.gvr.Resource, s.obj.GetName(), err)
		}
	}
	return &Service{Kube: &kube.Client{Clientset: cs, Dynamic: dyn}, Namespace: "kuso"}
}

type seed struct {
	gvr schema.GroupVersionResource
	obj *unstructured.Unstructured
}

func seedProject(name string, defaultBranch, repoURL string, installationID int64) seed {
	p := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: repoURL, DefaultBranch: defaultBranch},
		},
	}
	if installationID > 0 {
		p.Spec.GitHub = &kube.KusoProjectGithubSpec{InstallationID: installationID}
	}
	return typedSeed(kube.GVRProjects, "KusoProject", p)
}

func seedService(project, service string) seed {
	s := &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: project + "-" + service, Namespace: "kuso"},
		Spec: kube.KusoServiceSpec{
			Project: project,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/example/" + service, Path: "."},
		},
	}
	return typedSeed(kube.GVRServices, "KusoService", s)
}

func seedBuild(b *kube.KusoBuild) seed {
	return typedSeed(kube.GVRBuilds, "KusoBuild", b)
}

func seedProductionEnv(project, service string) seed {
	// Real envs are always labelled with project + service + env kind
	// (services_ops.AddService sets this when it auto-creates the
	// production env). Mirror that here so the build poller's
	// label-selected env list finds this fixture.
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-production",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": project,
				"kuso.sislelabs.com/service": service,
				"kuso.sislelabs.com/env":     "production",
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Kind: "production"},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func typedSeed(gvr schema.GroupVersionResource, kind string, obj any) seed {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if u.GetNamespace() == "" {
		u.SetNamespace("kuso")
	}
	return seed{gvr: gvr, obj: u}
}

// ---- pure helpers --------------------------------------------------------

func TestImageTag(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abcdef0123456789abcdef0123456789abcdef01": "abcdef012345",
		"main-abc":  "main-abc",
		"feat/x":    "feat/x", // not validated for branches
	}
	for in, want := range cases {
		if got := ImageTag(in); got != want {
			t.Errorf("ImageTag(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestShortRef_KubeNameSafe(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"abcdef0123456789abcdef0123456789abcdef01": "abcdef012345",
		"feat/x":           "feat-x",
		"FEAT/Long-Branch": "feat-long-branch",
	}
	for in, want := range cases {
		if got := shortRef(in); got != want {
			t.Errorf("shortRef(%q): got %q, want %q", in, got, want)
		}
	}
}

// ---- create --------------------------------------------------------------

func TestCreate_FullSHARef(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name != "alpha-web-abcdef012345" {
		t.Errorf("name: %q", got.Name)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != "abcdef012345" {
		t.Errorf("image tag: %+v", got.Spec.Image)
	}
	if got.Spec.Image.Repository != "kuso-registry.kuso.svc.cluster.local:5000/alpha/web" {
		t.Errorf("repo: %q", got.Spec.Image.Repository)
	}
	if got.Spec.Strategy != "dockerfile" {
		t.Errorf("strategy: %q", got.Spec.Strategy)
	}
	if got.Labels["kuso.sislelabs.com/build-ref"] != "abcdef012345" {
		t.Errorf("build-ref label: %v", got.Labels)
	}
}

func TestCreate_BranchOnly_SyntheticRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Branch: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Synthetic ref has the form main-<base36-unix-millis>; the prefix
	// is what we assert because the timestamp varies.
	if !strings.HasPrefix(got.Spec.Ref, "main-") {
		t.Errorf("synthetic ref: %q", got.Spec.Ref)
	}
	if got.Spec.Image.Tag != got.Spec.Ref {
		t.Errorf("non-SHA ref should pass through verbatim as tag: %q vs %q", got.Spec.Image.Tag, got.Spec.Ref)
	}
}

// TestCreate_DedupsConcurrentSameSHA checks that two concurrent
// Create calls for the same (project, service, sha) don't both spawn
// build CRs. The second caller should get ErrConflict back. This is
// the dedup that makes async webhook dispatch + GitHub retries safe.
//
// We simulate the race by pre-occupying the inFlight slot (as if a
// leader were mid-Create) and then releasing it on a delay. The
// follower's Create call should hit the dedup branch, wait on the
// leader's `done` channel, then return ErrConflict.
func TestCreate_DedupsConcurrentSameSHA(t *testing.T) {
	t.Parallel()
	const ref = "abcdef0123456789abcdef0123456789abcdef01"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)

	key := inFlightKey("alpha", "web", ref)
	entry := &inFlightEntry{done: make(chan struct{})}
	s.inFlight.Store(key, entry)

	// Release the slot after a short delay so the follower's
	// `<-prev.done` branch unblocks and returns ErrConflict.
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.inFlight.Delete(key)
		close(entry.done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Ref: ref})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

// TestCreate_SupersedesPriorInFlight verifies that creating a new
// build for (project, service) cancels any older in-flight build for
// the same pair. On a single-box install, two concurrent builds for
// the same service will starve the kuso-server pod for CPU and OOM
// the host (this is what we hit on v0.7.38 — three back-to-back
// redeploys held three kaniko Jobs simultaneously and tipped load
// avg past 13). The fix: stamp the predecessor as cancelled +
// build-state=done and delete its Job.
// TestCreate_QueuesSecondInFlight asserts the v0.8.6 behaviour:
// a Create call while another build for the same service is in flight
// stamps the new CR as queued (build-state=queued + phase=queued)
// instead of refusing it (v0.8.5) or silently superseding the prior
// build (pre-v0.8.5). The poller's dispatchQueued promotes it once
// the active build finishes.
func TestCreate_QueuesSecondInFlight(t *testing.T) {
	t.Parallel()
	const newRef = "1111111111111111111111111111111111111111"
	const oldName = "alpha-web-deadbeefcafe"
	prior := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      oldName,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
				// Crucially: no `kuso.sislelabs.com/build-state`
				// label — that's what the supersede selector looks
				// for to identify in-flight builds.
			},
		},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-web",
			Ref:     "deadbeefcafe",
			Branch:  "main",
		},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedBuild(prior),
	)
	// Seed the kaniko Job that the operator would have rendered for
	// the prior build. The supersede helper should delete it.
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: oldName, Namespace: "kuso"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed prior job: %v", err)
	}

	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: newRef})
	if err != nil {
		t.Fatalf("Create: want success (queued), got %v", err)
	}
	if got.Name == oldName {
		t.Fatalf("new build should differ from prior, got %q", got.Name)
	}
	// New build should be marked queued.
	rawNew, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), got.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get new: %v", err)
	}
	if state := rawNew.GetLabels()["kuso.sislelabs.com/build-state"]; state != "queued" {
		t.Errorf("new build state: got %q, want queued", state)
	}
	if phase := rawNew.GetAnnotations()[annPhase]; phase != "queued" {
		t.Errorf("new build phase: got %q, want queued", phase)
	}

	// Prior CR should be untouched (no phase=cancelled, no build-state).
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), oldName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get prior: %v", err)
	}
	if p := raw.GetAnnotations()[annPhase]; p == "cancelled" {
		t.Errorf("prior phase: should not be cancelled by Create, got %q", p)
	}
	if l := raw.GetLabels()["kuso.sislelabs.com/build-state"]; l != "" {
		t.Errorf("prior build-state: should be unset, got %q", l)
	}
	// Prior Job should still exist — no supersede.
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Get(context.Background(), oldName, metav1.GetOptions{}); err != nil {
		t.Errorf("prior job should remain, got %v", err)
	}
}

// TestCancel_StampsAndDeletes asserts Cancel marks the CR as
// phase=cancelled + build-state=done and deletes the kaniko Job.
func TestCancel_StampsAndDeletes(t *testing.T) {
	t.Parallel()
	const buildName = "alpha-web-cafebabe1234"
	prior := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildName,
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
			Annotations: map[string]string{
				annPhase: "running",
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "cafebabe1234", Branch: "main"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedBuild(prior),
	)
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: buildName, Namespace: "kuso"},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := s.Cancel(context.Background(), "alpha", "web", buildName); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), buildName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after cancel: %v", err)
	}
	annos := raw.GetAnnotations()
	if annos[annPhase] != "cancelled" {
		t.Errorf("phase: got %q, want cancelled", annos[annPhase])
	}
	if raw.GetLabels()["kuso.sislelabs.com/build-state"] != "done" {
		t.Errorf("build-state: got %q, want done", raw.GetLabels()["kuso.sislelabs.com/build-state"])
	}
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Get(context.Background(), buildName, metav1.GetOptions{}); err == nil {
		t.Errorf("job should be deleted after Cancel")
	}
	// Cancelling an already-cancelled build → ErrInvalid.
	if err := s.Cancel(context.Background(), "alpha", "web", buildName); !errors.Is(err, ErrInvalid) {
		t.Errorf("second Cancel: want ErrInvalid, got %v", err)
	}
}

// TestCreate_CoalescesRapidSyntheticRedeploys asserts that two
// back-to-back Redeploy clicks (no explicit ref) on the same service
// + branch return the same build CR — no second CR is created. The
// fix prevents 10 ghost rows piling up when a user spam-clicks the
// button.
func TestCreate_CoalescesRapidSyntheticRedeploys(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	first, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if first.Name != second.Name {
		t.Errorf("coalesce failed: first=%q second=%q (should be equal)", first.Name, second.Name)
	}
	// And only one build CR should exist.
	list, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list builds: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("want exactly 1 build, got %d", len(list.Items))
	}
}

// TestCreate_CoalesceWindowExpires asserts that after the window
// elapses, a fresh redeploy creates a new build (and the prior one
// queues behind it because the active-check fires).
func TestCreate_CoalesceWindowExpires(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	first, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	// Cheat: backdate the first CR's creationTimestamp past the window.
	raw, _ := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), first.Name, metav1.GetOptions{})
	old := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	raw.SetCreationTimestamp(old)
	if _, uerr := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Update(context.Background(), raw, metav1.UpdateOptions{}); uerr != nil {
		t.Fatalf("backdate: %v", uerr)
	}
	second, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if first.Name == second.Name {
		t.Errorf("coalesce should NOT have applied past window: both built %q", first.Name)
	}
}

// TestPoller_DispatchQueuedPromotesWhenIdle covers the dispatcher:
// when no active build exists for a service, the oldest queued build
// is promoted (build-state label removed, phase=pending stamped).
// Queued builds with an active sibling stay queued.
func TestPoller_DispatchQueuedPromotesWhenIdle(t *testing.T) {
	t.Parallel()
	idle := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-queued1",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project":     "alpha",
				"kuso.sislelabs.com/service":     "alpha-web",
				"kuso.sislelabs.com/build-state": "queued",
			},
			Annotations: map[string]string{annPhase: "queued"},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "queued1ref", Branch: "main"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedBuild(idle),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}
	p.dispatchQueued(context.Background(), "kuso")

	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), "alpha-web-queued1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after dispatch: %v", err)
	}
	if state := raw.GetLabels()["kuso.sislelabs.com/build-state"]; state != "" {
		t.Errorf("build-state after promote: got %q, want unset", state)
	}
	if phase := raw.GetAnnotations()[annPhase]; phase != "pending" {
		t.Errorf("phase after promote: got %q, want pending", phase)
	}
	// spec.image should be patched in — that's the chart's render gate.
	imgRepo, _, _ := unstructured.NestedString(raw.Object, "spec", "image", "repository")
	imgTag, _, _ := unstructured.NestedString(raw.Object, "spec", "image", "tag")
	if imgRepo == "" || imgTag == "" {
		t.Errorf("spec.image after promote: repo=%q tag=%q, want both populated", imgRepo, imgTag)
	}
}

// TestPoller_DispatchQueuedSkipsWhenActive covers the safety: a
// queued build sitting behind an active sibling should NOT be
// promoted on the next tick, otherwise we'd run two builds for the
// same service simultaneously.
func TestPoller_DispatchQueuedSkipsWhenActive(t *testing.T) {
	t.Parallel()
	active := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-active",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
			Annotations: map[string]string{annPhase: "running"},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "activeref", Branch: "main"},
	}
	queued := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-web-queued1",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project":     "alpha",
				"kuso.sislelabs.com/service":     "alpha-web",
				"kuso.sislelabs.com/build-state": "queued",
			},
			Annotations: map[string]string{annPhase: "queued"},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "queued1ref", Branch: "main"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedBuild(active),
		seedBuild(queued),
	)
	p := &Poller{Svc: s, Logger: slog.Default()}
	p.dispatchQueued(context.Background(), "kuso")

	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), "alpha-web-queued1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after dispatch: %v", err)
	}
	if state := raw.GetLabels()["kuso.sislelabs.com/build-state"]; state != "queued" {
		t.Errorf("queued build wrongly promoted while sibling active (state=%q)", state)
	}
}

// TestCreate_DoesNotConflictAcrossServices guards against the active-
// build check spuriously rejecting builds of a different service in
// the same project. The label selector pins on the FQ service name
// (`<project>-<service>`), so a build for `alpha-api` should not
// trigger a conflict when a new build for `alpha-web` arrives.
func TestCreate_DoesNotConflictAcrossServices(t *testing.T) {
	t.Parallel()
	const newRef = "2222222222222222222222222222222222222222"
	otherService := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-api-aaaaaaaaaaaa",
			Namespace: "kuso",
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-api", // different service
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-api"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
		seedBuild(otherService),
	)
	if _, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: newRef}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), "alpha-api-aaaaaaaaaaaa", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get other-service build: %v", err)
	}
	if phase := raw.GetAnnotations()[annPhase]; phase == "cancelled" {
		t.Errorf("other-service build was wrongly superseded (phase=%q)", phase)
	}
	if state := raw.GetLabels()["kuso.sislelabs.com/build-state"]; state != "" {
		t.Errorf("other-service build wrongly marked done (build-state=%q)", state)
	}
}

// TestCreate_ConcurrencyCapClusterReality exercises the v0.8.10
// admission gate: Create rejects with ErrConflict when the cluster
// already has MaxConcurrentBuilds running build pods. The gate
// counts pods (real cluster state) rather than an in-memory
// semaphore — survives kuso-server restart, catches operator-
// rendered Job pods, and reflects actual node load.
func TestCreate_ConcurrencyCapClusterReality(t *testing.T) {
	t.Parallel()
	const ref = "0011223344556677889900112233445566778899"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "api"),
		seedService("alpha", "web"),
	)
	s.MaxConcurrentBuilds = 1
	// Seed an existing running build pod for some other service. The
	// new admission gate sees this via pods.List(component=kusobuild)
	// and refuses to admit a second build cluster-wide.
	if _, err := s.Kube.Clientset.CoreV1().Pods("kuso").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-api-existing-buildpod",
			Namespace: "kuso",
			Labels: map[string]string{
				"app.kubernetes.io/component": "kusobuild",
				"kuso.sislelabs.com/project":  "alpha",
				"kuso.sislelabs.com/service":  "alpha-api",
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed running pod: %v", err)
	}

	_, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict (cap held by existing pod), got %v", err)
	}
}

// TestCreate_ConcurrencyCapAllowsWhenIdle: with no running build
// pods cluster-wide, Create proceeds normally even at MaxConcurrent=1.
func TestCreate_ConcurrencyCapAllowsWhenIdle(t *testing.T) {
	t.Parallel()
	const ref = "aabbccddeeff00112233445566778899aabbccdd"
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	s.MaxConcurrentBuilds = 1

	got, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{Ref: ref})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got == nil || got.Spec.Ref != ref {
		t.Errorf("expected build for %s, got %+v", ref, got)
	}
}

func TestCreate_NoServiceErrNotFound(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", "main", "https://github.com/x/y", 0))
	_, err := s.Create(context.Background(), "alpha", "ghost", CreateBuildRequest{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestCreate_NoRepoURLErrInvalid(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "", 0),
		typedSeed(kube.GVRServices, "KusoService", &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web", Namespace: "kuso"},
			Spec:       kube.KusoServiceSpec{Project: "alpha"}, // no repo
		}),
	)
	_, err := s.Create(context.Background(), "alpha", "web", CreateBuildRequest{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("got %v", err)
	}
}

// ---- list ----------------------------------------------------------------

func TestList_NewestFirst(t *testing.T) {
	t.Parallel()
	now := time.Now()
	older := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha-web-aaa",
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Hour)),
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "aaa"},
	}
	newer := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alpha-web-bbb",
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(now),
			Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha",
				"kuso.sislelabs.com/service": "alpha-web",
			},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "bbb"},
	}
	s := fakeService(t, seedBuild(older), seedBuild(newer))

	got, err := s.List(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: %d", len(got))
	}
	if got[0].Name != "alpha-web-bbb" {
		t.Errorf("expected newest first, got %v", []string{got[0].Name, got[1].Name})
	}
}

// ---- poller --------------------------------------------------------------

func TestPoller_PromotesImageOnSuccess(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-abc", Namespace: "kuso"},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-web",
			Ref:     "abc",
			Image:   &kube.KusoImage{Repository: "registry/alpha/web", Tag: "abc"},
		},
	}
	s := fakeService(t, seedBuild(build), seedProductionEnv("alpha", "web"))
	// Seed a completed Job that mirrors the kaniko output.
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-abc", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: "True"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Build status should be succeeded now.
	got, err := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-abc")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if got.Annotations[annPhase] != "succeeded" {
		t.Errorf("phase annotation: %v", got.Annotations)
	}

	// Production env's image should have been patched.
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if envCR.Spec.Image == nil || envCR.Spec.Image.Tag != "abc" {
		t.Errorf("env image not promoted: %+v", envCR.Spec.Image)
	}
}

func TestPoller_MarksFailed(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-fff", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "fff", Image: &kube.KusoImage{}},
	}
	s := fakeService(t, seedBuild(build))
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-fff", Namespace: "kuso"},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: "True", Message: "kaniko exit 1"},
			},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-fff")
	if got.Annotations[annPhase] != "failed" {
		t.Errorf("phase annotation: %v", got.Annotations)
	}
	if !strings.Contains(got.Annotations[annMessage], "kaniko") {
		t.Errorf("message annotation: %v", got.Annotations[annMessage])
	}
}

func TestPoller_MarksRunning(t *testing.T) {
	t.Parallel()
	build := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-rrr", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "rrr", Image: &kube.KusoImage{}},
	}
	s := fakeService(t, seedBuild(build))
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-rrr", Namespace: "kuso"},
		Status:     batchv1.JobStatus{Active: 1},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", "alpha-web-rrr")
	if got.Annotations[annPhase] != "running" {
		t.Errorf("phase annotation: %v", got.Annotations)
	}
}

func TestPoller_SkipsTerminal(t *testing.T) {
	t.Parallel()
	// A build already marked succeeded MUST NOT be re-poked — no Job
	// existing should be a no-op, not an error path.
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-old", Namespace: "kuso"},
		Spec:       kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web", Ref: "old", Image: &kube.KusoImage{}},
		Status:     map[string]any{"phase": "succeeded"},
	}
	s := fakeService(t, seedBuild(b))
	p := &Poller{Svc: s, Interval: time.Hour}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}
