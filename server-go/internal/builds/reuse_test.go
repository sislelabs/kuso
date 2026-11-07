package builds

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const reuseSHA = "0123456789abcdef0123456789abcdef01234567"

func reuseBuild(name, svc string, created time.Time) *kube.KusoBuild {
	return &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso",
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{kube.LabelProject: "puncho"},
			Annotations:       map[string]string{},
		},
		Spec: kube.KusoBuildSpec{
			Project: "puncho",
			Service: "puncho-" + svc,
			Repo:    &kube.KusoRepoRef{URL: "https://github.com/acme/puncho.git", Path: "app"},
			Ref:     reuseSHA,
			Branch:  "main",
			Image:   &kube.KusoImage{Repository: RegistryHost + "/puncho/" + svc, Tag: reuseSHA[:12]},
		},
	}
}

func TestReuseKey(t *testing.T) {
	now := time.Now()
	base := reuseBuild("puncho-web-x", "web", now)
	baseKey, ok := reuseKey(base)
	if !ok {
		t.Fatal("base build should be eligible")
	}
	other := reuseBuild("puncho-render-x", "render", now)
	other.Spec.Repo.URL = "git@github.com:Acme/puncho" // same repo, other spelling
	other.Spec.Strategy = "worker"
	if k, _ := reuseKey(other); k != baseKey {
		t.Error("services differing only in name/repo spelling/worker strategy should share a key")
	}

	ineligible := map[string]func(b *kube.KusoBuild){
		"short ref":         func(b *kube.KusoBuild) { b.Spec.Ref = "main" },
		"dry run":           func(b *kube.KusoBuild) { b.Spec.DryRun = true },
		"external registry": func(b *kube.KusoBuild) { b.Spec.Image.Repository = "ghcr.io/acme/web" },
		"registry auth":     func(b *kube.KusoBuild) { b.Spec.Auth = &kube.KusoBuildAuth{SecretName: "creds"} },
		"no image":          func(b *kube.KusoBuild) { b.Spec.Image = nil },
	}
	for name, mut := range ineligible {
		b := reuseBuild("b", "web", now)
		mut(b)
		if _, ok := reuseKey(b); ok {
			t.Errorf("%s: should not be eligible", name)
		}
	}

	differs := map[string]func(b *kube.KusoBuild){
		"sha":             func(b *kube.KusoBuild) { b.Spec.Ref = strings.Repeat("a", 40) },
		"path":            func(b *kube.KusoBuild) { b.Spec.Repo.Path = "worker" },
		"dockerfile":      func(b *kube.KusoBuild) { b.Spec.Dockerfile = "Dockerfile.render" },
		"build env":       func(b *kube.KusoBuild) { b.Spec.BuildEnv = map[string]string{"NEXT_PUBLIC_X": "1"} },
		"build env ref":   func(b *kube.KusoBuild) { b.Spec.BuildEnv = map[string]string{"DB": "kuso-secret-ref://a/DB"} },
		"build args":      func(b *kube.KusoBuild) { b.Spec.BuildArgs = map[string]string{"A": "1"} },
		"strategy":        func(b *kube.KusoBuild) { b.Spec.Strategy = "nixpacks" },
		"project":         func(b *kube.KusoBuild) { b.Spec.Project = "other" },
		"public env":      func(b *kube.KusoBuild) { b.Spec.PublicEnv = []string{"API_URL"} },
		"static settings": func(b *kube.KusoBuild) { b.Spec.Static = &kube.KusoStaticSpec{OutputDir: "dist"} },
	}
	for name, mut := range differs {
		b := reuseBuild("b", "render", now)
		mut(b)
		if k, ok := reuseKey(b); ok && k == baseKey {
			t.Errorf("%s: differing builds share a reuse key", name)
		}
	}
}

func TestReuseSource(t *testing.T) {
	t0 := time.Now().Add(-time.Minute)
	web := reuseBuild("puncho-web-x", "web", t0)
	render := reuseBuild("puncho-render-x", "render", t0.Add(time.Second))
	op := reuseBuild("puncho-operator-x", "operator", t0.Add(2*time.Second))
	all := []*kube.KusoBuild{op, render, web}

	if src := ReuseSource(web, all); src != nil {
		t.Errorf("earliest build must build for real, got source %s", src.Name)
	}
	for _, b := range []*kube.KusoBuild{render, op} {
		if src := ReuseSource(b, all); src == nil || src.Name != web.Name {
			t.Errorf("%s: want source %s, got %v", b.Name, web.Name, src)
		}
	}

	// Same creation second: name breaks the tie, in one direction only.
	a := reuseBuild("puncho-a-x", "a", t0)
	if src := ReuseSource(a, all); src != nil {
		t.Errorf("tie: name-earlier build must not reuse, got %s", src.Name)
	}
	if src := ReuseSource(web, append(all, a)); src == nil || src.Name != a.Name {
		t.Errorf("tie: want %s as source, got %v", a.Name, src)
	}

	failed := reuseBuild("puncho-web-x", "web", t0)
	failed.Annotations[annPhase] = "failed"
	if src := ReuseSource(op, []*kube.KusoBuild{failed, render}); src == nil || src.Name != render.Name {
		t.Errorf("failed source must be skipped for the next earliest, got %v", src)
	}

	reuser := reuseBuild("puncho-render-x", "render", t0.Add(time.Second))
	reuser.Annotations[AnnReuseFrom] = "gone"
	if src := ReuseSource(op, []*kube.KusoBuild{reuser}); src != nil {
		t.Errorf("a reusing build must never be a source, got %s", src.Name)
	}

	old := reuseBuild("puncho-web-old", "web", time.Now().Add(-7*time.Hour))
	if src := ReuseSource(op, []*kube.KusoBuild{old}); src != nil {
		t.Errorf("source older than 6h must be skipped")
	}

	sameSvc := reuseBuild("puncho-operator-old", "operator", t0)
	if src := ReuseSource(op, []*kube.KusoBuild{sameSvc}); src != nil {
		t.Errorf("same-service build must not be a source")
	}

	fellBack := reuseBuild("puncho-operator-x", "operator", t0.Add(2*time.Second))
	fellBack.Annotations[AnnReuseFallback] = "copy failed"
	if src := ReuseSource(fellBack, all); src != nil {
		t.Errorf("a fallen-back build must not choose reuse again")
	}
}

func TestCopyImage(t *testing.T) {
	const ociManifest = "application/vnd.oci.image.manifest.v1+json"
	const ociIndex = "application/vnd.oci.image.index.v1+json"
	manifest := func(cfg string, layers ...string) []byte {
		ls := make([]string, len(layers))
		for i, l := range layers {
			ls[i] = fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q}`, l)
		}
		return []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"config":{"digest":%q},"layers":[%s]}`,
			ociManifest, cfg, strings.Join(ls, ",")))
	}

	t.Run("single manifest", func(t *testing.T) {
		f := newFakeRegistry()
		dg := f.putImage("puncho/web", "abc", ociManifest, manifest("sha256:c1", "sha256:l1", "sha256:l2"),
			"sha256:c1", "sha256:l1", "sha256:l2")
		f.blobs["puncho/render@sha256:l1"] = true // already present: skipped, not re-mounted
		c := startFakeRegistry(t, f).(*registryClient)
		got, err := c.CopyImage(context.Background(), "puncho/web", "abc", "puncho/render", "abc")
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		if got != dg || f.tags["puncho/render"]["abc"] != dg {
			t.Errorf("dst digest %s / tag %s, want %s", got, f.tags["puncho/render"]["abc"], dg)
		}
		if ct := f.manifests["puncho/render@"+dg].contentType; ct != ociManifest {
			t.Errorf("content type not preserved: %q", ct)
		}
	})

	t.Run("index", func(t *testing.T) {
		f := newFakeRegistry()
		amd := f.putImage("puncho/web", "", ociManifest, manifest("sha256:ca", "sha256:la"), "sha256:ca", "sha256:la")
		arm := f.putImage("puncho/web", "", ociManifest, manifest("sha256:cb", "sha256:lb"), "sha256:cb", "sha256:lb")
		idx := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":%q,"manifests":[{"digest":%q},{"digest":%q}]}`, ociIndex, amd, arm))
		dg := f.putImage("puncho/web", "abc", ociIndex, idx)
		c := startFakeRegistry(t, f).(*registryClient)
		got, err := c.CopyImage(context.Background(), "puncho/web", "abc", "puncho/render", "def")
		if err != nil {
			t.Fatalf("copy: %v", err)
		}
		if got != dg || f.tags["puncho/render"]["def"] != dg {
			t.Errorf("index digest %s, want %s", got, dg)
		}
		for _, child := range []string{amd, arm} {
			if _, ok := f.manifests["puncho/render@"+child]; !ok {
				t.Errorf("child %s not copied", child)
			}
		}
	})

	t.Run("mount not done is an error", func(t *testing.T) {
		f := newFakeRegistry()
		f.putImage("puncho/web", "abc", ociManifest, manifest("sha256:c1", "sha256:l1"), "sha256:c1", "sha256:l1")
		f.mountStatus = http.StatusAccepted
		c := startFakeRegistry(t, f).(*registryClient)
		_, err := c.CopyImage(context.Background(), "puncho/web", "abc", "puncho/render", "abc")
		if err == nil || !strings.Contains(err.Error(), "mount") {
			t.Fatalf("expected a mount error when the registry answers 202, got %v", err)
		}
		if _, ok := f.tags["puncho/render"]["abc"]; ok {
			t.Error("tag written despite failed mount")
		}
	})

	t.Run("destination digest mismatch is an error", func(t *testing.T) {
		f := newFakeRegistry()
		f.putImage("puncho/web", "abc", ociManifest, manifest("sha256:c1"), "sha256:c1")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPut {
				w = digestRewriter{w}
			}
			f.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		c := newRegistryClient(strings.TrimPrefix(srv.URL, "http://"))
		if _, err := c.CopyImage(context.Background(), "puncho/web", "abc", "puncho/render", "abc"); err == nil {
			t.Fatal("expected error when the copied digest differs from the source")
		}
	})

	t.Run("missing source tag is an error", func(t *testing.T) {
		c := startFakeRegistry(t, newFakeRegistry()).(*registryClient)
		if _, err := c.CopyImage(context.Background(), "puncho/web", "nope", "puncho/render", "abc"); err == nil {
			t.Fatal("expected error for a missing source tag")
		}
	})
}

// digestRewriter makes a registry report a different manifest digest.
type digestRewriter struct{ http.ResponseWriter }

func (d digestRewriter) WriteHeader(code int) {
	d.Header().Set("Docker-Content-Digest", "sha256:other")
	d.ResponseWriter.WriteHeader(code)
}

type fakeCopier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeCopier) CopyImage(_ context.Context, srcRepo, srcTag, dstRepo, dstTag string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, srcRepo+":"+srcTag+"->"+dstRepo+":"+dstTag)
	return "sha256:same", f.err
}

func seedSucceededJob(t *testing.T, s *Service, name string) {
	t.Helper()
	if _, err := s.Kube.Clientset.BatchV1().Jobs("kuso").Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Status:     batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: "True"}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
}

func TestPoller_ReuseCopiesAndPromotesOwnImage(t *testing.T) {
	t0 := time.Now().Add(-time.Minute)
	web := reuseBuild("puncho-web-x", "web", t0)
	web.Annotations[annPhase] = "succeeded"
	web.Labels[LabelBuildState] = BuildStateDone
	render := reuseBuild("puncho-render-x", "render", t0.Add(time.Second))
	render.Annotations[AnnReuseFrom] = web.Name
	render.Annotations[annPhase] = "running"
	s := fakeService(t, seedBuild(web), seedBuild(render), seedProductionEnv("puncho", "render"))
	cp := &fakeCopier{}
	p := &Poller{Svc: s, Interval: time.Hour, ImageCopier: cp}
	if err := p.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	drainPromotions(t, p)

	want := "puncho/web:" + reuseSHA[:12] + "->puncho/render:" + reuseSHA[:12]
	if len(cp.calls) != 1 || cp.calls[0] != want {
		t.Fatalf("copy calls %v, want [%s]", cp.calls, want)
	}
	got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", render.Name)
	if buildPhase(got) != "succeeded" || got.Labels[LabelBuildState] != BuildStateDone {
		t.Errorf("reuser not succeeded: phase=%q labels=%v", buildPhase(got), got.Labels)
	}
	if !strings.Contains(got.Annotations[annMessage], "reused image from web") {
		t.Errorf("message = %q", got.Annotations[annMessage])
	}
	env, _ := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "puncho-render-production")
	if env.Spec.Image == nil || env.Spec.Image.Repository != RegistryHost+"/puncho/render" || env.Spec.Image.Tag != reuseSHA[:12] {
		t.Errorf("render env not promoted to its own repo/tag: %+v", env.Spec.Image)
	}
}

func TestPoller_ReuseFallsBack(t *testing.T) {
	cases := map[string]struct {
		sourcePhase string
		copyErr     error
		noSource    bool
	}{
		"source failed":    {sourcePhase: "failed"},
		"source cancelled": {sourcePhase: "cancelled"},
		"source deleted":   {noSource: true},
		"copy error":       {sourcePhase: "succeeded", copyErr: errors.New("tag gone")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t0 := time.Now().Add(-time.Hour) // past the 30m no-Job stuck timeout
			web := reuseBuild("puncho-web-x", "web", t0)
			web.Annotations[annPhase] = tc.sourcePhase
			render := reuseBuild("puncho-render-x", "render", t0.Add(time.Second))
			render.Annotations[AnnReuseFrom] = web.Name
			render.Annotations[annPhase] = "running"
			seeds := []seed{seedBuild(render), seedProductionEnv("puncho", "render")}
			if !tc.noSource {
				seeds = append(seeds, seedBuild(web))
			}
			s := fakeService(t, seeds...)
			p := &Poller{Svc: s, Interval: time.Hour, ImageCopier: &fakeCopier{err: tc.copyErr}}
			if err := p.checkBuild(context.Background(), "kuso", render); err != nil {
				t.Fatalf("checkBuild: %v", err)
			}
			got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", render.Name)
			if got.Annotations[AnnReuseFrom] != "" || got.Annotations[AnnReuseFallback] == "" {
				t.Fatalf("no fallback: %v", got.Annotations)
			}
			if ph := buildPhase(got); ph != "pending" {
				t.Errorf("phase after fallback = %q, want pending", ph)
			}
			// The next tick, before the controller renders the Job, must not
			// force-fail the build on the stuck-timeout.
			if err := p.checkBuild(context.Background(), "kuso", got); err != nil {
				t.Fatalf("second checkBuild: %v", err)
			}
			got, _ = s.Kube.GetKusoBuild(context.Background(), "kuso", render.Name)
			if ph := buildPhase(got); ph != "pending" {
				t.Errorf("fallen-back build force-failed while awaiting its Job: phase=%q", ph)
			}
		})
	}
}

// The same-repo promotion hold keeps a source non-terminal until every
// sibling of the SHA is green, and the reuser is such a sibling. Both
// must reach succeeded: the reuser keys readiness off the source's
// Job-succeeded stamp, not its phase.
func TestPoller_ReuseUnderSiblingHoldNoDeadlock(t *testing.T) {
	t0 := time.Now().Add(-time.Minute)
	web := reuseBuild("puncho-web-x", "web", t0)
	web.Annotations[annPhase] = "running"
	render := reuseBuild("puncho-render-x", "render", t0.Add(time.Second))
	render.Annotations[AnnReuseFrom] = web.Name
	render.Annotations[annPhase] = "running"
	s := fakeService(t, seedBuild(web), seedBuild(render),
		seedProductionEnv("puncho", "web"), seedProductionEnv("puncho", "render"))
	seedSucceededJob(t, s, web.Name)
	p := &Poller{Svc: s, Interval: time.Hour, ImageCopier: &fakeCopier{}}
	for i := 0; i < 6; i++ {
		if err := p.tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		drainPromotions(t, p)
	}
	for _, name := range []string{web.Name, render.Name} {
		got, _ := s.Kube.GetKusoBuild(context.Background(), "kuso", name)
		if buildPhase(got) != "succeeded" {
			t.Errorf("%s: phase=%q hold=%q — sibling hold deadlocked", name, buildPhase(got), got.Annotations[annPromoteHold])
		}
	}
}
