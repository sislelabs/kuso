package previewdb

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

func previewEnvCR(name string, connSecrets []string, releaseCmd []string, imageTag string) kube.KusoEnvironment {
	e := kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Kind:           "preview",
			EnvFromSecrets: connSecrets,
		},
	}
	if len(releaseCmd) > 0 {
		e.Spec.Release = &kube.KusoReleaseSpec{Command: releaseCmd}
	}
	if imageTag != "" {
		e.Spec.Image = &kube.KusoImage{Repository: "registry/app", Tag: imageTag}
	}
	return e
}

// TestSelectMigratableEnvs picks only the preview envs that (a) mount THIS
// clone's conn secret, (b) carry a release command, and (c) have an image —
// the join that decides which services migrate against a given clone.
func TestSelectMigratableEnvs(t *testing.T) {
	t.Parallel()
	clone := "tickero-db-pr-36"
	conn := "tickero-db-pr-36-conn"
	migrateCmd := []string{"sh", "-c", "migrate up"}

	envs := []kube.KusoEnvironment{
		// api: mounts the clone, has release + image → SELECTED
		previewEnvCR("tickero-api-pr-36", []string{conn, "shared-secrets"}, migrateCmd, "abc123"),
		// frontend: mounts NO db conn, no release → skipped
		previewEnvCR("tickero-frontend-pr-36", []string{"shared-secrets"}, nil, "def456"),
		// backoffice: mounts the clone but has no release hook → skipped
		previewEnvCR("tickero-backoffice-pr-36", []string{conn}, nil, "ghi789"),
		// api but no image yet (build not promoted) → skipped (can't migrate)
		previewEnvCR("tickero-api2-pr-36", []string{conn}, migrateCmd, ""),
		// references a DIFFERENT clone → skipped
		previewEnvCR("tickero-other-pr-36", []string{"tickero-other-pr-36-conn"}, migrateCmd, "xyz000"),
	}

	got := selectMigratableEnvs(envs, addons.ConnSecretName(clone))
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	want := []string{"tickero-api-pr-36"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("selectMigratableEnvs = %v, want %v", names, want)
	}
}

// TestBuildMigrateJob_RunsReleaseCmdAgainstClone verifies the post-seed
// migrate Job: it runs the env's release command in the env's PR image, mounts
// the clone's conn secret (so DATABASE_URL points at the clone), is one-shot,
// and is owned by the clone addon for cascade-on-PR-close.
func TestBuildMigrateJob_RunsReleaseCmdAgainstClone(t *testing.T) {
	t.Parallel()
	conn := "tickero-db-pr-36-conn"
	cmd := []string{"sh", "-c", "migrate -path /app/migrations -database \"$DATABASE_URL\" up"}
	env := previewEnvCR("tickero-api-pr-36", []string{conn, "tickero-shared-secrets"}, cmd, "e41610fef3bb")

	job := buildMigrateJob("kuso", "tickero", "tickero-db-pr-36", &env, "owner-uid-9", 1780434239)

	// One-shot: never retry a half-applied migration.
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Errorf("backoffLimit must be 0, got %v", job.Spec.BackoffLimit)
	}
	pod := job.Spec.Template.Spec
	if len(pod.Containers) != 1 {
		t.Fatalf("want 1 container, got %d", len(pod.Containers))
	}
	c := pod.Containers[0]
	// Runs the env's release command in the env's PR image.
	if c.Image != "registry/app:e41610fef3bb" {
		t.Errorf("image = %q, want registry/app:e41610fef3bb", c.Image)
	}
	if strings.Join(c.Command, " ") != strings.Join(cmd, " ") {
		t.Errorf("command = %v, want %v", c.Command, cmd)
	}
	// Mounts the clone conn (DATABASE_URL resolves to the clone), AND any
	// other env-from the env carries.
	var sawConn bool
	for _, ef := range c.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == conn {
			sawConn = true
		}
	}
	if !sawConn {
		t.Errorf("migrate container must mount the clone conn secret %q via envFrom; got %+v", conn, c.EnvFrom)
	}
	// MUST have a wait-for-addons init container that TCP-waits on the
	// clone DB before running migrate — the clone StatefulSet's ClusterIP
	// transiently refuses connections while it comes up / reconciles, and
	// with backoffLimit=0 a connection-refused permanently fails the
	// migrate (the live bug: "dial tcp ...:5432: connect: connection
	// refused"). The init must inherit the same envFrom so $DATABASE_URL
	// resolves.
	if len(pod.InitContainers) == 0 {
		t.Fatalf("migrate Job must have a wait-for-addons init container; got none")
	}
	wait := pod.InitContainers[0]
	if wait.Name != "wait-for-addons" {
		t.Errorf("first init container = %q, want wait-for-addons", wait.Name)
	}
	var initSawConn bool
	for _, ef := range wait.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == conn {
			initSawConn = true
		}
	}
	if !initSawConn {
		t.Errorf("wait-for-addons init must inherit the clone conn envFrom so $DATABASE_URL resolves; got %+v", wait.EnvFrom)
	}
	// Owned by the clone addon CR (cascade on PR-close).
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Name != "tickero-db-pr-36" {
		t.Errorf("owner ref must be the clone addon, got %+v", job.OwnerReferences)
	}
	// TTL-reaped so resync runs don't accumulate.
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Errorf("migrate Job must set TTLSecondsAfterFinished")
	}
}

// TestBuildMigrateJob_SeedNonceMakesReopenReMigrate is the regression test for
// the close→reopen bug: a re-seed (new nonce) must produce a DISTINCT migrate
// Job name so it actually re-runs, instead of fast-pathing to a stale prior
// run keyed on (env, image-tag) — the v0.18.9 idempotency trap.
func TestBuildMigrateJob_SeedNonceMakesReopenReMigrate(t *testing.T) {
	t.Parallel()
	conn := "tickero-db-pr-36-conn"
	cmd := []string{"sh", "-c", "migrate up"}
	env := previewEnvCR("tickero-api-pr-36", []string{conn}, cmd, "e41610fef3bb")

	first := buildMigrateJob("kuso", "tickero", "tickero-db-pr-36", &env, "uid", 1780434239)
	reopen := buildMigrateJob("kuso", "tickero", "tickero-db-pr-36", &env, "uid", 1780434999)

	if first.Name == reopen.Name {
		t.Errorf("re-seed must yield a distinct migrate Job name (same image tag must NOT dedupe); both were %q", first.Name)
	}
}

// TestSeedInFlightGuard dedupes concurrent seed+migrate spawns for the SAME
// clone: ensurePreviewEnv calls EnsurePRAddons once per service, so multiple
// services sharing one DB addon would each spawn a seed+migrate goroutine for
// the same clone (observed: 3 redundant migrate Jobs per reopen). The guard
// lets only the first caller proceed until it releases.
func TestSeedInFlightGuard(t *testing.T) {
	t.Parallel()
	c := &Cloner{}
	clone := "tickero-db-pr-36"

	if !c.tryAcquireSeed(clone) {
		t.Fatal("first acquire should succeed")
	}
	if c.tryAcquireSeed(clone) {
		t.Error("second acquire for the same clone (in flight) must be refused")
	}
	// A different clone is independent.
	if !c.tryAcquireSeed("tickero-db-pr-99") {
		t.Error("a different clone must acquire independently")
	}
	// After release, the clone can be acquired again (a later genuine resync).
	c.releaseSeed(clone)
	if !c.tryAcquireSeed(clone) {
		t.Error("after release, the clone must be re-acquirable")
	}
}

// TestEnvNeedsMigrate separates "this env should migrate against the clone (it
// mounts the clone + has a release hook)" from "has an image yet". The image
// is promoted by the build poller asynchronously and may land AFTER the seed
// completes — so migrateAfterSeed must WAIT for the image rather than skip the
// env outright (the bug the job-dedupe exposed: migrate ran pre-image and did
// nothing, and no redundant goroutine re-ran it).
func TestEnvNeedsMigrate(t *testing.T) {
	t.Parallel()
	conn := "tickero-db-pr-36-conn"
	cmd := []string{"sh", "-c", "migrate up"}

	// mounts clone + has release, image not yet stamped → STILL needs migrate
	noImage := previewEnvCR("tickero-api-pr-36", []string{conn}, cmd, "")
	if !envNeedsMigrate(&noImage, conn) {
		t.Error("env that mounts the clone + has a release must need migrate even before its image lands")
	}
	// with image → needs migrate
	withImage := previewEnvCR("tickero-api-pr-36", []string{conn}, cmd, "abc")
	if !envNeedsMigrate(&withImage, conn) {
		t.Error("env with clone+release+image must need migrate")
	}
	// no release → never migrates
	noRelease := previewEnvCR("tickero-frontend-pr-36", []string{conn}, nil, "abc")
	if envNeedsMigrate(&noRelease, conn) {
		t.Error("env without a release hook must NOT need migrate")
	}
	// different clone → not this clone's concern
	otherClone := previewEnvCR("tickero-x-pr-36", []string{"other-conn"}, cmd, "abc")
	if envNeedsMigrate(&otherClone, conn) {
		t.Error("env that does not mount this clone must NOT need migrate")
	}
}

func newFakeCloner(t *testing.T, envObjs ...*kube.KusoEnvironment) *Cloner {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	for _, e := range envObjs {
		m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(e)
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVREnvironments.GroupVersion().WithKind("KusoEnvironment"))
		if u.GetNamespace() == "" {
			u.SetNamespace("kuso")
		}
		if err := dyn.Tracker().Create(kube.GVREnvironments, u, "kuso"); err != nil {
			t.Fatalf("seed env: %v", err)
		}
	}
	return &Cloner{
		Kube:      &kube.Client{Clientset: fake.NewSimpleClientset(), Dynamic: dyn},
		Namespace: "kuso",
		Logger:    slog.Default(),
		BaseCtx:   context.Background(),
	}
}

// TestWaitForEnvImage_ReturnsOnceImageLands is the regression test for the
// image-after-seed race that the job-dedupe exposed: the build poller stamps
// spec.image asynchronously, possibly AFTER the seed completes, so the migrate
// path must wait for the image rather than skip the env.
func TestWaitForEnvImage_ReturnsOnceImageLands(t *testing.T) {
	t.Parallel()
	noImage := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "tickero-api-pr-36", Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Kind: "preview"},
	}
	c := newFakeCloner(t, noImage)

	// No image yet → times out fast, returns nil.
	if env := c.waitForEnvImage(context.Background(), "kuso", "tickero-api-pr-36", 80*time.Millisecond); env != nil {
		t.Fatalf("should time out to nil while no image is stamped, got %+v", env.Spec.Image)
	}

	// Stamp the image, then the wait should return it.
	stamped := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "tickero-api-pr-36", Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Kind:  "preview",
			Image: &kube.KusoImage{Repository: "registry/app", Tag: "e41610fef3bb"},
		},
	}
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(stamped)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVREnvironments.GroupVersion().WithKind("KusoEnvironment"))
	if _, err := c.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace("kuso").
		Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("stamp image: %v", err)
	}
	env := c.waitForEnvImage(context.Background(), "kuso", "tickero-api-pr-36", 2*time.Second)
	if env == nil || env.Spec.Image == nil || env.Spec.Image.Tag != "e41610fef3bb" {
		t.Fatalf("should return the env once image is stamped, got %+v", env)
	}
}
