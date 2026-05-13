package buildcontroller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// baseBuild returns a minimal valid build CR for tests.
func baseBuild() *kube.KusoBuild {
	return &kube.KusoBuild{
		Spec: kube.KusoBuildSpec{
			Project:  "alpha",
			Service:  "api",
			Ref:      "0123456789abcdef0123456789abcdef01234567",
			Branch:   "main",
			Strategy: "dockerfile",
			Repo:     &kube.KusoRepoRef{URL: "https://github.com/owner/repo.git"},
			Image:    &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
		},
	}
}

func TestStrategyDefault(t *testing.T) {
	cases := map[string]string{
		"":            "dockerfile",
		"dockerfile":  "dockerfile",
		"DockerFile":  "dockerfile",
		"nixpacks":    "nixpacks",
		"buildpacks":  "buildpacks",
		"static":      "static",
		"unknown":     "dockerfile",
	}
	for in, want := range cases {
		b := baseBuild()
		b.Spec.Strategy = in
		if got := strategyOf(b); got != want {
			t.Errorf("strategy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuoteEscape(t *testing.T) {
	cases := map[string]string{
		"main":            `'main'`,
		"foo'bar":         `'foo'\''bar'`,
		"":                `''`,
		`'; rm -rf / ; '`: `''\''; rm -rf / ; '\'''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderServiceAccount(t *testing.T) {
	owner := metav1.OwnerReference{Name: "b1"}
	sa := renderServiceAccount("b1", "kuso-alpha", owner)
	if sa.Name != "b1-runner" {
		t.Errorf("sa name: %q", sa.Name)
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("automount should be explicitly false")
	}
	if len(sa.OwnerReferences) != 1 {
		t.Errorf("ownerrefs: %+v", sa.OwnerReferences)
	}
}

func TestRenderJobBasic(t *testing.T) {
	b := baseBuild()
	owner := metav1.OwnerReference{Name: "b1", UID: "uid-1"}
	job := renderJob("b1", "kuso-alpha", b, owner)

	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("backoffLimit must be 0 (no retry)")
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 3600 {
		t.Error("ttl must be 3600")
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 3600 {
		t.Error("activeDeadlineSeconds must be 3600")
	}
	pod := job.Spec.Template.Spec
	if pod.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q", pod.RestartPolicy)
	}
	if pod.ServiceAccountName != "b1-runner" {
		t.Errorf("sa = %q", pod.ServiceAccountName)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("automount on pod spec must be false")
	}
	// Expected init containers for default (dockerfile, no cache):
	// clone + env-detect (no cache-init, no nixpacks-plan, no static-plan).
	names := initContainerNames(pod)
	wantInits := []string{"clone", "env-detect"}
	if !sliceEq(names, wantInits) {
		t.Errorf("init containers = %v, want %v", names, wantInits)
	}
	// Primary container: buildkit (default for non-buildpacks).
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "buildkit" {
		t.Errorf("containers = %v", pod.Containers)
	}
	// Affinity + toleration.
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil {
		t.Error("nodeAffinity missing")
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Key != "kuso.sislelabs.com/build" {
		t.Errorf("tolerations = %+v", pod.Tolerations)
	}
}

func TestRenderJobNixpacksWithCache(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "nixpacks"
	b.Spec.Cache = &kube.KusoBuildCache{PVCName: "alpha-cache"}
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	pod := job.Spec.Template.Spec
	inits := initContainerNames(pod)
	want := []string{"cache-init", "clone", "env-detect", "nixpacks-plan"}
	if !sliceEq(inits, want) {
		t.Errorf("nixpacks+cache init containers = %v, want %v", inits, want)
	}
	if !volumeExists(pod, "cache") {
		t.Error("cache volume missing")
	}
}

func TestRenderJobStatic(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "static"
	b.Spec.Static = &kube.KusoStaticSpec{
		BuildCmd:  "npm run build",
		OutputDir: "dist",
	}
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	pod := job.Spec.Template.Spec
	inits := initContainerNames(pod)
	want := []string{"clone", "env-detect", "static-plan"}
	if !sliceEq(inits, want) {
		t.Errorf("static init containers = %v, want %v", inits, want)
	}
	staticPlan := findInit(pod, "static-plan")
	if staticPlan == nil {
		t.Fatal("static-plan missing")
	}
	// BuildCmd flows as env var (not as a positional shell expr).
	if !containsEnv(staticPlan.Env, "BUILD_CMD", "npm run build") {
		t.Errorf("BUILD_CMD env not stamped: %+v", staticPlan.Env)
	}
	if !containsEnv(staticPlan.Env, "OUTPUT_DIR", "dist") {
		t.Errorf("OUTPUT_DIR env not stamped")
	}
}

func TestRenderJobBuildpacks(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "buildpacks"
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	pod := job.Spec.Template.Spec
	// Primary container should be `buildpacks` (CNB lifecycle creator).
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "buildpacks" {
		t.Errorf("containers = %v", pod.Containers)
	}
	// Volumes include `layers` and `cnb-cache`.
	if !volumeExists(pod, "layers") {
		t.Error("layers volume missing")
	}
	if !volumeExists(pod, "cnb-cache") {
		t.Error("cnb-cache volume missing")
	}
}

func TestRenderJobPrivateRepoSecretRef(t *testing.T) {
	b := baseBuild()
	b.Spec.GithubInstallationID = 12345
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	clone := findInit(job.Spec.Template.Spec, "clone")
	if clone == nil {
		t.Fatal("clone missing")
	}
	var tokenRef *corev1.EnvVar
	for i := range clone.Env {
		if clone.Env[i].Name == "GITHUB_INSTALLATION_TOKEN" {
			tokenRef = &clone.Env[i]
		}
	}
	if tokenRef == nil {
		t.Fatal("GITHUB_INSTALLATION_TOKEN env missing on private-repo clone")
	}
	if tokenRef.ValueFrom == nil || tokenRef.ValueFrom.SecretKeyRef == nil {
		t.Fatal("token env should be a secretKeyRef")
	}
	if tokenRef.ValueFrom.SecretKeyRef.Name != "b1-token" {
		t.Errorf("token secret ref name = %q", tokenRef.ValueFrom.SecretKeyRef.Name)
	}
	// Script body should NOT splice the repo URL or branch literally
	// into a shell command — it should go through shellQuote so an
	// embedded `'` can't break out.
	if !strings.Contains(clone.Args[0], `URL='`) {
		t.Errorf("clone script missing quoted URL: %s", clone.Args[0])
	}
}

func TestRenderJobAuthSecretMount(t *testing.T) {
	b := baseBuild()
	b.Spec.Auth = &kube.KusoBuildAuth{SecretName: "ghcr-pull", Registry: "ghcr.io"}
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	if !volumeExists(job.Spec.Template.Spec, "docker-config") {
		t.Error("docker-config volume missing under non-buildpacks strategy")
	}
	bk := findContainer(job.Spec.Template.Spec, "buildkit")
	if bk == nil {
		t.Fatal("buildkit missing")
	}
	if !mountExists(bk.VolumeMounts, "docker-config") {
		t.Error("buildkit should mount docker-config when auth.secretName set")
	}
}

func TestRenderJobBuildpacksOmitsDockerConfig(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "buildpacks"
	b.Spec.Auth = &kube.KusoBuildAuth{SecretName: "ghcr-pull"}
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	if volumeExists(job.Spec.Template.Spec, "docker-config") {
		t.Error("buildpacks strategy must not mount docker-config (uses CNB_REGISTRY_AUTH env)")
	}
	bp := findContainer(job.Spec.Template.Spec, "buildpacks")
	if bp == nil {
		t.Fatal("buildpacks missing")
	}
	var found bool
	for _, e := range bp.Env {
		if e.Name == "CNB_REGISTRY_AUTH" {
			found = true
		}
	}
	if !found {
		t.Error("CNB_REGISTRY_AUTH env missing")
	}
}

func TestRenderJobLabelsRoundTrip(t *testing.T) {
	b := baseBuild()
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	want := map[string]string{
		"app.kubernetes.io/name":        "kusobuild",
		"app.kubernetes.io/component":   "kusobuild",
		"app.kubernetes.io/managed-by":  "kuso",
		// `instance=<build-name>` is the critical selector key used
		// by logs/stream + builds.Cancel + drift. The helm chart
		// stamped this automatically from .Release.Name; the Go
		// controller has to set it explicitly. Regression here
		// breaks the Deployments-tab log viewer for every build.
		"app.kubernetes.io/instance":   "b1",
		"kuso.sislelabs.com/project":   "alpha",
		"kuso.sislelabs.com/service":   "api",
		"kuso.sislelabs.com/build-ref": baseBuild().Spec.Ref,
	}
	for k, v := range want {
		if got := job.Labels[k]; got != v {
			t.Errorf("job label %s = %q, want %q", k, got, v)
		}
	}
	// And on the pod template — log selectors hit the pod, not the
	// Job itself.
	if got := job.Spec.Template.Labels["app.kubernetes.io/instance"]; got != "b1" {
		t.Errorf("pod template label app.kubernetes.io/instance = %q, want b1", got)
	}
}

func TestResourceRequirementsDefaults(t *testing.T) {
	b := baseBuild()
	res, err := resourceRequirements(b)
	if err != nil {
		t.Fatal(err)
	}
	cpu := res.Requests[corev1.ResourceCPU]
	if cpu.String() != "200m" {
		t.Errorf("default cpu request = %s", cpu.String())
	}
	mem := res.Limits[corev1.ResourceMemory]
	if mem.String() != "2Gi" {
		t.Errorf("default mem limit = %s", mem.String())
	}
}

func TestResourceRequirementsOverride(t *testing.T) {
	b := baseBuild()
	b.Spec.Resources = &kube.KusoBuildResources{
		Requests: &kube.KusoResourceQty{CPU: "500m", Memory: "1Gi"},
		Limits:   &kube.KusoResourceQty{CPU: "4", Memory: "8Gi"},
	}
	res, err := resourceRequirements(b)
	if err != nil {
		t.Fatal(err)
	}
	cpu := res.Requests[corev1.ResourceCPU]
	if cpu.String() != "500m" {
		t.Errorf("override cpu request = %s", cpu.String())
	}
	memL := res.Limits[corev1.ResourceMemory]
	if memL.String() != "8Gi" {
		t.Errorf("override mem limit = %s", memL.String())
	}
}

// --- helpers -----------------------------------------------------

func initContainerNames(p corev1.PodSpec) []string {
	out := make([]string, len(p.InitContainers))
	for i, c := range p.InitContainers {
		out[i] = c.Name
	}
	return out
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findInit(p corev1.PodSpec, name string) *corev1.Container {
	for i := range p.InitContainers {
		if p.InitContainers[i].Name == name {
			return &p.InitContainers[i]
		}
	}
	return nil
}

func findContainer(p corev1.PodSpec, name string) *corev1.Container {
	for i := range p.Containers {
		if p.Containers[i].Name == name {
			return &p.Containers[i]
		}
	}
	return nil
}

func volumeExists(p corev1.PodSpec, name string) bool {
	for _, v := range p.Volumes {
		if v.Name == name {
			return true
		}
	}
	return false
}

func mountExists(mounts []corev1.VolumeMount, name string) bool {
	for _, m := range mounts {
		if m.Name == name {
			return true
		}
	}
	return false
}

func containsEnv(env []corev1.EnvVar, name, value string) bool {
	for _, e := range env {
		if e.Name == name && e.Value == value {
			return true
		}
	}
	return false
}
