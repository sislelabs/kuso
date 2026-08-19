package buildcontroller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/builds"
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
		"":           "dockerfile",
		"dockerfile": "dockerfile",
		"DockerFile": "dockerfile",
		"nixpacks":   "nixpacks",
		"buildpacks": "buildpacks",
		"static":     "static",
		"unknown":    "dockerfile",
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
		if clone.Env[i].Name == "KUSO_GIT_TOKEN" {
			tokenRef = &clone.Env[i]
		}
	}
	if tokenRef == nil {
		t.Fatal("KUSO_GIT_TOKEN env missing on private-repo clone")
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
		"app.kubernetes.io/name":       "kusobuild",
		"app.kubernetes.io/component":  "kusobuild",
		"app.kubernetes.io/managed-by": "kuso",
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

// TestBuildEnvContainerVars verifies the service's LITERAL build-time env is
// passed as KUSO_BE_<KEY> vars + a KUSO_BUILDENV_KEYS list (kubelet-escaped
// values, no shell injection), that non-identifier keys are dropped at the
// render boundary, and that kuso-secret-ref:// values never flow through the
// literal channel (everything in KUSO_BUILDENV_KEYS persists into the
// published image as build-args / ENV lines).
func TestBuildEnvContainerVars(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		BuildEnv: map[string]string{
			"MAIL_FROM":           "Sender <noreply@x.example.com>",
			"NEXT_PUBLIC_APP_URL": "https://x.example.com",
			"BAD$(x)":             "evil", // dropped
			// secret-sourced (as builds.buildEnvFromVars stamps it) — must be
			// excluded here and handled only by buildEnvSecretContainerVars.
			"DATABASE_URL": builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
			// prefix-carrying junk: dropped entirely, never demoted to literal.
			"SNEAKY": "kuso-secret-ref://not a valid ref",
		},
	}}
	vars := buildEnvContainerVars(b)
	got := map[string]string{}
	for _, e := range vars {
		got[e.Name] = e.Value
	}
	if got["KUSO_BE_MAIL_FROM"] != "Sender <noreply@x.example.com>" {
		t.Errorf("MAIL_FROM not passed: %q", got["KUSO_BE_MAIL_FROM"])
	}
	if got["KUSO_BE_NEXT_PUBLIC_APP_URL"] != "https://x.example.com" {
		t.Errorf("NEXT_PUBLIC_APP_URL not passed: %q", got["KUSO_BE_NEXT_PUBLIC_APP_URL"])
	}
	if _, bad := got["KUSO_BE_BAD$(x)"]; bad {
		t.Error("malicious key must be dropped")
	}
	if _, leaked := got["KUSO_BE_DATABASE_URL"]; leaked {
		t.Error("secret-ref value must not flow through the literal channel")
	}
	if _, leaked := got["KUSO_BE_SNEAKY"]; leaked {
		t.Error("malformed secret-ref must be dropped, not demoted to literal")
	}
	keys := got["KUSO_BUILDENV_KEYS"]
	if keys != "MAIL_FROM NEXT_PUBLIC_APP_URL" {
		t.Errorf("KUSO_BUILDENV_KEYS = %q, want sorted literal keys only", keys)
	}
}

// TestBuildEnvSecretContainerVars: kuso-secret-ref:// entries render as
// kubelet secretKeyRef env mounts (no plaintext anywhere in the Job) plus
// the KUSO_BUILDENV_SECRET_KEYS list the buildkit script turns into
// `--secret id=<KEY>,env=KUSO_BE_<KEY>` flags. Literals and malformed refs
// must not appear.
func TestBuildEnvSecretContainerVars(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		BuildEnv: map[string]string{
			"DATABASE_URL":        builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
			"REDIS_PASSWORD":      builds.BuildEnvSecretRef("foo-cache-conn", "password"),
			"NEXT_PUBLIC_APP_URL": "https://x.example.com",             // literal → excluded
			"SNEAKY":              "kuso-secret-ref://not a valid ref", // malformed → dropped
		},
	}}
	vars := buildEnvSecretContainerVars(b)
	byName := map[string]corev1.EnvVar{}
	for _, e := range vars {
		byName[e.Name] = e
	}
	db, ok := byName["KUSO_BE_DATABASE_URL"]
	if !ok {
		t.Fatal("KUSO_BE_DATABASE_URL secret env missing")
	}
	if db.Value != "" || db.ValueFrom == nil || db.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("secret env must be a secretKeyRef, got %+v", db)
	}
	if db.ValueFrom.SecretKeyRef.Name != "foo-db-conn" || db.ValueFrom.SecretKeyRef.Key != "DATABASE_URL" {
		t.Errorf("secretKeyRef = %+v", db.ValueFrom.SecretKeyRef)
	}
	if db.ValueFrom.SecretKeyRef.Optional == nil || !*db.ValueFrom.SecretKeyRef.Optional {
		t.Error("secret env must be optional (missing secret degrades to omit, not CreateContainerConfigError)")
	}
	if _, ok := byName["KUSO_BE_NEXT_PUBLIC_APP_URL"]; ok {
		t.Error("literal must not appear in the secret channel")
	}
	if _, ok := byName["KUSO_BE_SNEAKY"]; ok {
		t.Error("malformed ref must be dropped")
	}
	if got := byName["KUSO_BUILDENV_SECRET_KEYS"].Value; got != "DATABASE_URL REDIS_PASSWORD" {
		t.Errorf("KUSO_BUILDENV_SECRET_KEYS = %q, want sorted secret keys", got)
	}
}

// TestBuildkitContainerSecretsNeverBuildArgs is the regression test for the
// credential-baking leak: secretKeyRef-sourced env used to reach the buildkit
// invocation as plaintext `--opt build-arg:` values, which buildkit records
// in the pushed image's config/history — anyone with registry read access
// could recover live addon credentials. Now a secret-sourced entry must
// render as a secretKeyRef env mount (never a literal), be absent from
// KUSO_BUILDENV_KEYS (the build-arg list), present in
// KUSO_BUILDENV_SECRET_KEYS (the --secret list), and the script must forward
// it via the non-persisting buildkit secret mechanism.
func TestBuildkitContainerSecretsNeverBuildArgs(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "dockerfile",
		BuildEnv: map[string]string{
			"DATABASE_URL":        builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
			"NEXT_PUBLIC_APP_URL": "https://x.example.com",
		},
		Image: &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
	}}
	c := renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{})

	var keys, secretKeys string
	for _, e := range c.Env {
		switch e.Name {
		case "KUSO_BUILDENV_KEYS":
			keys = e.Value
		case "KUSO_BUILDENV_SECRET_KEYS":
			secretKeys = e.Value
		case "KUSO_BE_DATABASE_URL":
			if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
				t.Errorf("secret env must be a secretKeyRef mount, got %+v", e)
			}
		}
		// No env var on the container may carry the ref as a literal value
		// (the literal channel is what feeds --opt build-arg).
		if strings.Contains(e.Value, "foo-db-conn/DATABASE_URL") {
			t.Errorf("secret ref leaked as literal env %s=%q", e.Name, e.Value)
		}
	}
	if keys != "NEXT_PUBLIC_APP_URL" {
		t.Errorf("KUSO_BUILDENV_KEYS = %q — secret key must not be in the build-arg list", keys)
	}
	if secretKeys != "DATABASE_URL" {
		t.Errorf("KUSO_BUILDENV_SECRET_KEYS = %q", secretKeys)
	}
	// Script must forward secrets via buildkit's secret mechanism, sourced
	// from the KUSO_BE_* env, and never interpolate values.
	if !strings.Contains(c.Args[0], `--secret "id=${k},env=KUSO_BE_${k}"`) {
		t.Error("buildkit script does not forward KUSO_BUILDENV_SECRET_KEYS as --secret flags")
	}
	if !strings.Contains(c.Args[0], "KUSO_BUILDENV_SECRET_KEYS") {
		t.Error("buildkit script does not consume KUSO_BUILDENV_SECRET_KEYS")
	}
}

// TestBuildkitNonDockerfileStrategiesWithholdSecrets: nixpacks/static build
// kuso-GENERATED Dockerfiles that cannot opt into RUN --mount=type=secret,
// so the secret values must be withheld from the buildkit pod entirely for
// those strategies — not merely kept out of the build-arg list.
func TestBuildkitNonDockerfileStrategiesWithholdSecrets(t *testing.T) {
	for _, strategy := range []string{"nixpacks", "static"} {
		b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
			Strategy: strategy,
			BuildEnv: map[string]string{
				"DATABASE_URL": builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
			},
			Image: &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
		}}
		c := renderBuildkitContainer(b, strategy, corev1.ResourceRequirements{})
		for _, e := range c.Env {
			if e.Name == "KUSO_BE_DATABASE_URL" || e.Name == "KUSO_BUILDENV_SECRET_KEYS" {
				t.Errorf("%s: secret env %s must not be mounted", strategy, e.Name)
			}
		}
	}
}

// TestBuildkitWarnsOnWithheldSecrets: withholding secret-sourced build env
// is a silent breaking change without a signal — a Dockerfile still reading
// `ARG DATABASE_URL` builds with an empty value, and a nixpacks/static build
// that needs a secret at build time just builds without it. The build LOG
// (which users already view) must therefore name the withheld keys and the
// migration path. Key NAMES only — never values or secret refs.
func TestBuildkitWarnsOnWithheldSecrets(t *testing.T) {
	secretEnv := map[string]string{
		"DATABASE_URL":        builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
		"REDIS_PASSWORD":      builds.BuildEnvSecretRef("foo-cache-conn", "password"),
		"NEXT_PUBLIC_APP_URL": "https://x.example.com", // literal → not withheld
	}

	// nixpacks/static withhold the secrets from the pod entirely: the key
	// NAMES must surface via KUSO_BUILDENV_WITHHELD_KEYS so the script's
	// WARNING block fires.
	for _, strategy := range []string{"nixpacks", "static"} {
		b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
			Strategy: strategy,
			BuildEnv: secretEnv,
			Image:    &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
		}}
		c := renderBuildkitContainer(b, strategy, corev1.ResourceRequirements{})
		var withheld string
		for _, e := range c.Env {
			if e.Name == "KUSO_BUILDENV_WITHHELD_KEYS" {
				withheld = e.Value
			}
			if strings.Contains(e.Value, "foo-db-conn") || strings.Contains(e.Value, "kuso-secret-ref://") {
				t.Errorf("%s: env %s leaks the secret ref (%q) — key NAMES only", strategy, e.Name, e.Value)
			}
		}
		if withheld != "DATABASE_URL REDIS_PASSWORD" {
			t.Errorf("%s: KUSO_BUILDENV_WITHHELD_KEYS = %q, want sorted withheld key names", strategy, withheld)
		}
	}

	// dockerfile forwards the secrets as buildkit secret mounts; the
	// warning there rides KUSO_BUILDENV_SECRET_KEYS, so the withheld list
	// must be absent (no double signal).
	bDf := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "dockerfile",
		BuildEnv: secretEnv,
		Image:    &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
	}}
	for _, e := range renderBuildkitContainer(bDf, "dockerfile", corev1.ResourceRequirements{}).Env {
		if e.Name == "KUSO_BUILDENV_WITHHELD_KEYS" {
			t.Errorf("dockerfile: KUSO_BUILDENV_WITHHELD_KEYS present (%q) — dockerfile secrets are forwarded, not withheld", e.Value)
		}
	}

	// No secret-sourced vars → no withheld list (and the runtime warning
	// blocks are gated on non-empty vars, so nothing fires).
	bNone := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "nixpacks",
		BuildEnv: map[string]string{"NEXT_PUBLIC_APP_URL": "https://x.example.com"},
		Image:    &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
	}}
	for _, e := range renderBuildkitContainer(bNone, "nixpacks", corev1.ResourceRequirements{}).Env {
		if e.Name == "KUSO_BUILDENV_WITHHELD_KEYS" {
			t.Errorf("no-secret build: KUSO_BUILDENV_WITHHELD_KEYS present (%q), want absent", e.Value)
		}
	}

	// The script itself must carry both warning blocks: the build-arg→
	// secret-mount migration hint (dockerfile) and the withheld-at-build-
	// time explanation (nixpacks/static).
	script := renderBuildkitContainer(bDf, "dockerfile", corev1.ResourceRequirements{}).Args[0]
	for _, want := range []string{
		"KUSO_BUILDENV_SECRET_KEYS:-",
		"KUSO_BUILDENV_WITHHELD_KEYS:-",
		"WARNING",
		"--mount=type=secret,id=<KEY>",
		"EMPTY value",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("buildkit script missing warning fragment %q", want)
		}
	}
}

// TestNixpacksPlanWithholdsSecrets: the nixpacks-plan container bakes every
// KUSO_BE_* it receives into permanent ENV layers of the generated
// Dockerfile, so secret-sourced entries must be absent from its env and from
// KUSO_BUILDENV_KEYS.
func TestNixpacksPlanWithholdsSecrets(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "nixpacks",
		BuildEnv: map[string]string{
			"DATABASE_URL":        builds.BuildEnvSecretRef("foo-db-conn", "DATABASE_URL"),
			"NEXT_PUBLIC_APP_URL": "https://x.example.com",
		},
	}}
	c := renderNixpacksPlanContainer(b)
	for _, e := range c.Env {
		if e.Name == "KUSO_BE_DATABASE_URL" || e.Name == "KUSO_BUILDENV_SECRET_KEYS" {
			t.Errorf("nixpacks-plan must not receive secret env %s", e.Name)
		}
		if e.Name == "KUSO_BUILDENV_KEYS" && e.Value != "NEXT_PUBLIC_APP_URL" {
			t.Errorf("KUSO_BUILDENV_KEYS = %q, want literals only", e.Value)
		}
	}
}

// TestNixpacksContainerInjectsBuildEnv: the rendered nixpacks-plan container
// carries the buildEnv vars + the script reads KUSO_BUILDENV_KEYS.
// TestBuildkitContainerInjectsBuildArgs is the regression test for the
// dockerfile build-env gap: build-time env injection used to work only for
// nixpacks/static (which generate a Dockerfile kuso can edit), NOT for raw
// Dockerfile builds — the buildkit invocation passed zero build env. So a
// dockerfile app whose Dockerfile declares `ARG DATABASE_URL` (and a build
// step that reads it, e.g. `RUN npm run build` validating env) got an empty
// value and failed. The buildkit container must now carry the KUSO_BE_*
// vars AND the script must pass each buildEnv key as `--opt build-arg`.
// Passing a build-arg the Dockerfile doesn't declare is a harmless no-op in
// buildkit, so passing all keys is safe.
func TestBuildkitContainerInjectsBuildArgs(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "dockerfile",
		BuildEnv: map[string]string{"DATABASE_URL": "postgres://x", "STRIPE_SECRET_KEY": "sk_test_x"},
		Image:    &kube.KusoImage{Repository: "registry.local/alpha/api", Tag: "sha"},
	}}
	c := renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{})

	// The KUSO_BE_* values must be present as container env (kubelet
	// escapes them; the script reads them via printenv).
	var sawKey bool
	for _, e := range c.Env {
		if e.Name == "KUSO_BE_DATABASE_URL" && e.Value == "postgres://x" {
			sawKey = true
		}
	}
	if !sawKey {
		t.Error("buildkit container missing KUSO_BE_DATABASE_URL")
	}
	// The script must consume the keys and pass them as build-args.
	if !strings.Contains(c.Args[0], "KUSO_BUILDENV_KEYS") {
		t.Error("buildkit script does not consume KUSO_BUILDENV_KEYS")
	}
	if !strings.Contains(c.Args[0], "build-arg") {
		t.Error("buildkit script does not pass --opt build-arg for build env")
	}
}

func TestNixpacksContainerInjectsBuildEnv(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "nixpacks",
		BuildEnv: map[string]string{"DATABASE_URL": "postgres://x"},
	}}
	c := renderNixpacksPlanContainer(b)
	var sawKey bool
	for _, e := range c.Env {
		if e.Name == "KUSO_BE_DATABASE_URL" && e.Value == "postgres://x" {
			sawKey = true
		}
	}
	if !sawKey {
		t.Error("nixpacks container missing KUSO_BE_DATABASE_URL")
	}
	if !strings.Contains(c.Args[0], "KUSO_BUILDENV_KEYS") {
		t.Error("nixpacks script does not consume KUSO_BUILDENV_KEYS")
	}
}

// TestNixpacksBuildEnvFlagsAreWordSplitSafe is the regression test for the
// "RESEND_FROM=Name <email>" build failure. A build-env VALUE containing a
// space was concatenated into a NIXPACKS_ENV_FLAGS string and passed
// UNQUOTED to `nixpacks build . --out . $NIXPACKS_ENV_FLAGS`, so the value
// word-split and `<email>` was parsed as a stray nixpacks argument
// (exit 2). The fix builds the --env flags as positional params (set --)
// and passes "$@", so each --env KEY=VALUE stays one argv pair regardless
// of spaces in VALUE. Assert the unsafe unquoted-expansion pattern is gone.
func TestNixpacksBuildEnvFlagsAreWordSplitSafe(t *testing.T) {
	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{
		Strategy: "nixpacks",
		BuildEnv: map[string]string{"RESEND_FROM": "Bozhidar <bozhidar@launchpaid.app>"},
	}}
	c := renderNixpacksPlanContainer(b)
	script := c.Args[0]
	// The unquoted word-splitting form must NOT be present.
	if strings.Contains(script, "--out . $NIXPACKS_ENV_FLAGS") {
		t.Error("nixpacks build still uses unquoted $NIXPACKS_ENV_FLAGS — a value with spaces will word-split")
	}
	// The safe positional-param form must be present.
	if !strings.Contains(script, `nixpacks build . --out . "$@"`) {
		t.Error("nixpacks build does not pass env flags via quoted \"$@\"")
	}
}

// TestRenderJobGitLabPrivateRepo covers the GitLab clone path: a repo on a
// GitLab host with a stored token secret must render a private clone that
// mounts KUSO_GIT_TOKEN and uses GitLab's oauth2:<tok> auth form (not
// GitHub's x-access-token).
func TestRenderJobGitLabPrivateRepo(t *testing.T) {
	b := baseBuild()
	b.Spec.GithubInstallationID = 0
	b.Spec.Repo = &kube.KusoRepoRef{
		URL:         "https://gitlab.com/group/app.git",
		TokenSecret: "alpha-api-gitlab-token",
	}
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	clone := findInit(job.Spec.Template.Spec, "clone")
	if clone == nil {
		t.Fatal("clone missing")
	}
	// Token mounted as KUSO_GIT_TOKEN from the <build>-token Secret.
	var hasToken bool
	for i := range clone.Env {
		if clone.Env[i].Name == "KUSO_GIT_TOKEN" {
			hasToken = true
			if clone.Env[i].ValueFrom.SecretKeyRef.Name != "b1-token" {
				t.Errorf("token ref name = %q, want b1-token", clone.Env[i].ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	if !hasToken {
		t.Fatal("GitLab private clone must mount KUSO_GIT_TOKEN")
	}
	// GitLab auth form: oauth2:<tok>, NOT x-access-token.
	if !strings.Contains(clone.Args[0], "oauth2:${KUSO_GIT_TOKEN}") {
		t.Errorf("GitLab clone should use oauth2: auth, script:\n%s", clone.Args[0])
	}
	if strings.Contains(clone.Args[0], "x-access-token") {
		t.Errorf("GitLab clone must NOT use GitHub's x-access-token: %s", clone.Args[0])
	}
}

// TestRenderJobGitHubStillUsesXAccessToken guards that the GitHub path is
// unchanged: x-access-token auth, KUSO_GIT_TOKEN mount.
func TestRenderJobGitHubStillUsesXAccessToken(t *testing.T) {
	b := baseBuild()
	b.Spec.GithubInstallationID = 999
	job := renderJob("b1", "kuso-alpha", b, metav1.OwnerReference{Name: "b1"})
	clone := findInit(job.Spec.Template.Spec, "clone")
	if !strings.Contains(clone.Args[0], "x-access-token:${KUSO_GIT_TOKEN}") {
		t.Errorf("GitHub clone should keep x-access-token auth:\n%s", clone.Args[0])
	}
}
