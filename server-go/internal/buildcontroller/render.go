package buildcontroller

import (
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// renderServiceAccount mirrors the chart's per-build SA:
// `<build>-runner`, no bindings, automount=false. The SA's identity
// matters even with automount=false because admission webhooks
// (Kyverno / OPA / Pod Security) evaluate against it — a kaniko
// node-escape can't borrow the namespace's `default` SA grants if
// the build pod runs as a freshly-minted SA with no bindings.
func renderServiceAccount(buildName, ns string, owner metav1.OwnerReference) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildName + "-runner",
			Namespace:       ns,
			OwnerReferences: []metav1.OwnerReference{owner},
			Labels:          map[string]string{"app.kubernetes.io/managed-by": "kuso"},
		},
		AutomountServiceAccountToken: ptrFalse(),
	}
}

// renderJob mirrors templates/job.yaml. The conditional branches on
// strategy match the chart's `{{- if eq .Values.strategy ... }}`
// gates: nixpacks-plan / static-plan are init containers, buildpacks
// creator is a primary container that replaces buildkit when the
// strategy is buildpacks. errors from resourceRequirements bubble up
// — but kuso-server already validates resource shapes before stamping
// the CR, so a parse failure here implies an external apply.
func renderJob(buildName, ns string, b *kube.KusoBuild, owner metav1.OwnerReference) *batchv1.Job {
	strategy := strategyOf(b)
	labels := kusoBuildLabels(b, buildName)
	res, _ := resourceRequirements(b) // error already vetted server-side

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            buildName,
			Namespace:       ns,
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptrInt32(jobBackoffLimit),
			TTLSecondsAfterFinished: ptrInt32(jobTTLSecondsAfter),
			// ActiveDeadlineSeconds caps a stuck build at 1h. Without
			// it a nixpacks build with a broken Dockerfile can hold a
			// node for hours waiting for an apt mirror. Cancel still
			// works at any time; this is the kubelet-side timeout.
			ActiveDeadlineSeconds: ptrInt64(int64(jobActiveBudgetMins) * 60),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: renderPodSpec(buildName, b, strategy, res),
			},
		},
	}
	return job
}

// renderPodSpec assembles the pod spec — init containers + primary
// container, volumes, tolerations, affinity, security context.
func renderPodSpec(buildName string, b *kube.KusoBuild, strategy string, res corev1.ResourceRequirements) corev1.PodSpec {
	spec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		// Pod-level fsGroup=1000 makes the cache PVC's mount point
		// group-writable by GID 1000 (the cache-init / nixpacks-plan
		// runAsUser). runAsNonRoot intentionally NOT set at pod
		// level — env-detect baked in ripgrep/jq runs as 1000, but
		// clone (alpine/git) and nixpacks-plan need root for apk add.
		// Each long-running container drops to non-root via its own
		// securityContext.
		SecurityContext: &corev1.PodSecurityContext{
			FSGroup: ptrInt64(1000),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		ServiceAccountName:           buildName + "-runner",
		AutomountServiceAccountToken: ptrFalse(),
		// Build-pool steering: prefer nodes labelled
		// kuso.sislelabs.com/build=true. Soft preference so the Job
		// still schedules on a vanilla cluster with no build pool.
		// Toleration is unconditional so a tainted build node accepts
		// these Jobs.
		Tolerations: []corev1.Toleration{
			{
				Key:      "kuso.sislelabs.com/build",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			},
		},
		Affinity: &corev1.Affinity{
			NodeAffinity: &corev1.NodeAffinity{
				PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
					{
						Weight: 100,
						Preference: corev1.NodeSelectorTerm{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "kuso.sislelabs.com/build",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"true"},
								},
							},
						},
					},
				},
			},
		},
		Volumes: renderVolumes(b, strategy),
	}

	// Init containers in chart order:
	//   1. cache-init (only when a PVC is attached)
	//   2. clone (always)
	//   3. env-detect (always)
	//   4. nixpacks-plan (only when strategy=nixpacks)
	//   5. static-plan (only when strategy=static)
	var inits []corev1.Container
	if hasCache(b) {
		inits = append(inits, renderCacheInitContainer())
	}
	inits = append(inits, renderCloneContainer(buildName, b))
	inits = append(inits, renderEnvDetectContainer(b))
	if strategy == "nixpacks" {
		inits = append(inits, renderNixpacksPlanContainer(b))
	}
	if strategy == "static" {
		inits = append(inits, renderStaticPlanContainer(b))
	}
	spec.InitContainers = inits

	// Primary container: buildpacks creator OR buildkit client.
	if strategy == "buildpacks" {
		spec.Containers = []corev1.Container{renderBuildpacksContainer(buildName, b, res)}
	} else {
		spec.Containers = []corev1.Container{renderBuildkitContainer(b, strategy, res)}
	}

	return spec
}

// dropAllCapsNonRoot is the securityContext we apply to every
// container that doesn't need root. Pinned here so a future tighten
// (e.g. seccompProfile per container) lands in one place.
func dropAllCapsNonRoot(uid, gid int64) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrFalse(),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		RunAsUser:  ptrInt64(uid),
		RunAsGroup: ptrInt64(gid),
	}
}

// dropAllCapsRootAllowed is for containers that need root (apk add
// at runtime — clone, nixpacks-plan). allowPrivilegeEscalation is
// still false; we just don't pin runAsUser=1000.
func dropAllCapsRootAllowed() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrFalse(),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// renderVolumes assembles the volumes array. Workspace is always
// present (emptyDir shared by every container). cache PVC, docker-
// config Secret, layers/cnb-cache (buildpacks) are conditional.
func renderVolumes(b *kube.KusoBuild, strategy string) []corev1.Volume {
	vols := []corev1.Volume{
		{
			Name:         "workspace",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}
	if hasCache(b) {
		vols = append(vols, corev1.Volume{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: b.Spec.Cache.PVCName,
				},
			},
		})
	}
	if hasAuthSecret(b) && strategy != "buildpacks" {
		// kaniko/buildkit reads /tmp/.docker/config.json from the
		// SA's docker-config Secret. Buildpacks reads creds inline
		// via the CNB_REGISTRY_AUTH env, so the Secret mount is
		// skipped for buildpacks.
		vols = append(vols, corev1.Volume{
			Name: "docker-config",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: b.Spec.Auth.SecretName,
					Items: []corev1.KeyToPath{
						{Key: ".dockerconfigjson", Path: "config.json"},
					},
				},
			},
		})
	}
	if strategy == "buildpacks" {
		vols = append(vols, corev1.Volume{
			Name:         "layers",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		if hasCache(b) {
			vols = append(vols, corev1.Volume{
				Name: "cnb-cache",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: b.Spec.Cache.PVCName,
					},
				},
			})
		} else {
			vols = append(vols, corev1.Volume{
				Name:         "cnb-cache",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			})
		}
	}
	return vols
}

func hasAuthSecret(b *kube.KusoBuild) bool {
	return b != nil && b.Spec.Auth != nil && b.Spec.Auth.SecretName != ""
}

// renderCacheInitContainer mirrors the chart's `cache-init`. Idempotent
// mkdir + best-effort chmod. Runs as 1000:1000 so PSS-restricted
// namespaces accept it.
func renderCacheInitContainer() corev1.Container {
	return corev1.Container{
		Name:            "cache-init",
		Image:           defaultCacheInitImage,
		SecurityContext: dropAllCapsNonRoot(1000, 1000),
		Command:         []string{"/bin/sh", "-c"},
		Args: []string{`
set -e
mkdir -p /cache/nix /cache/deps/npm /cache/deps/go-mod \
         /cache/deps/go-build /cache/deps/pip \
         /cache/deps/cargo /cache/deps/gradle /cache/deps/m2
chmod -R g+w /cache/nix /cache/deps 2>/dev/null || true
du -sh /cache/* 2>/dev/null || true
`},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "cache", MountPath: "/cache"},
		},
	}
}

// renderCloneContainer is the always-on git clone init. Private repos
// (githubInstallationId > 0) read GITHUB_INSTALLATION_TOKEN from the
// chart-rendered <build>-token Secret; the kuso-server build poller
// mints that token at CR-create time so we don't have to.
func renderCloneContainer(buildName string, b *kube.KusoBuild) corev1.Container {
	repoURL := ""
	branch := "main"
	ref := ""
	if b != nil && b.Spec.Repo != nil {
		repoURL = b.Spec.Repo.URL
	}
	if b != nil {
		if b.Spec.Branch != "" {
			branch = b.Spec.Branch
		}
		ref = b.Spec.Ref
	}
	private := b != nil && b.Spec.GithubInstallationID > 0

	// Build the clone script. We assemble it as a string with the
	// values quoted via Go's %q so a malicious repo URL or branch
	// (validated upstream but defense-in-depth) doesn't break out
	// of the quotes.
	cloneCmd := ""
	if private {
		cloneCmd = fmt.Sprintf(`
if [ -z "$GITHUB_INSTALLATION_TOKEN" ]; then
  echo "ERROR: GITHUB_INSTALLATION_TOKEN must be set for private repos"
  exit 1
fi
URL=%s
BRANCH=%s
git clone --depth 1 --branch "$BRANCH" \
  "https://x-access-token:${GITHUB_INSTALLATION_TOKEN}@$(echo "$URL" | sed -E 's|^https?://||')" \
  /workspace/src
`, shellQuote(repoURL), shellQuote(branch))
	} else {
		cloneCmd = fmt.Sprintf(`
git clone --depth 1 --branch %s %s /workspace/src
`, shellQuote(branch), shellQuote(repoURL))
	}

	script := `set -e
cd /workspace
` + cloneCmd + `
cd /workspace/src
REF=` + shellQuote(ref) + `
if echo "$REF" | grep -Eq '^[0-9a-f]{40}$'; then
  if ! git checkout "$REF" 2>/dev/null; then
    echo "checkout $REF failed; deepening clone"
    git fetch --unshallow
    git checkout "$REF"
  fi
else
  echo "ref $REF is not a SHA; using branch HEAD"
fi
echo "checked out: $(git rev-parse HEAD)"
`

	c := corev1.Container{
		Name:            "clone",
		Image:           defaultCloneImage,
		SecurityContext: dropAllCapsRootAllowed(),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{script},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
		},
	}
	if private {
		c.Env = []corev1.EnvVar{
			{
				Name: "GITHUB_INSTALLATION_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: buildName + "-token"},
						Key:                  "token",
					},
				},
			},
		}
	}
	return c
}

// renderEnvDetectContainer runs the env-detect baked image. Output
// goes to stdout bracketed by KUSO_ENV_DETECT_BEGIN/END sentinels;
// the build poller parses that out of the pod logs.
func renderEnvDetectContainer(b *kube.KusoBuild) corev1.Container {
	image := defaultEnvDetectImage + ":" + defaultEnvDetectTag
	path := repoPath(b)

	// The script is unchanged from the chart — it's a self-contained
	// bash blob that runs ripgrep + jq from the baked image. We pass
	// the repo path via env so it's quote-isolated, defense-in-depth
	// over our server-side validateRepoPath.
	script := `set -e
SRC="/workspace/src/$REPO_PATH"
cd "$SRC"

FROM_DOTENV=""
for f in .env.example .env.template .env.sample .env.dist; do
  if [ -f "$f" ]; then
    while IFS= read -r line; do
      case "$line" in
        \#*|"") continue ;;
      esac
      name="${line%%=*}"
      name="${name## }"; name="${name%% }"
      if echo "$name" | grep -qE '^[A-Z][A-Z0-9_]*$'; then
        FROM_DOTENV="${FROM_DOTENV}${name}\n"
      fi
    done < "$f"
    echo "env-detect: read $f"
  fi
done

GREP_GLOBS="-g !node_modules -g !vendor -g !.git -g !dist -g !build -g !.next -g !target -g !.venv -g !__pycache__"
{
  rg -oN --no-heading $GREP_GLOBS 'process\.env\.[A-Z][A-Z0-9_]*' 2>/dev/null | sed -E 's/.*process\.env\.([A-Z][A-Z0-9_]*).*/\1/'
  rg -oN --no-heading $GREP_GLOBS 'import\.meta\.env\.[A-Z][A-Z0-9_]*' 2>/dev/null | sed -E 's/.*import\.meta\.env\.([A-Z][A-Z0-9_]*).*/\1/'
  rg -oN --no-heading $GREP_GLOBS 'os\.getenv\(["A-Z_0-9]+' 2>/dev/null | grep -oE '[A-Z][A-Z0-9_]+'
  rg -oN --no-heading $GREP_GLOBS 'os\.environ\[' 2>/dev/null | grep -oE '[A-Z][A-Z0-9_]{2,}' | sort -u
  rg -oN --no-heading $GREP_GLOBS 'os\.Getenv\("[A-Z][A-Z0-9_]*"' 2>/dev/null | grep -oE '"[A-Z][A-Z0-9_]+"' | tr -d '"'
  rg -oN --no-heading $GREP_GLOBS 'ENV\[' 2>/dev/null | grep -oE '[A-Z][A-Z0-9_]{2,}' | sort -u
  rg -oN --no-heading $GREP_GLOBS 'System\.getenv\("[A-Z][A-Z0-9_]*"' 2>/dev/null | grep -oE '"[A-Z][A-Z0-9_]+"' | tr -d '"'
} > /tmp/grep-raw 2>/dev/null || true

RESERVED="PORT HOSTNAME HOME PATH USER PWD SHELL TERM LANG LC_ALL LC_CTYPE NODE_ENV NODE_OPTIONS NODE_VERSION NPM_CONFIG_LOGLEVEL DEBIAN_FRONTEND DEBUG CI VERCEL_ENV NEXT_RUNTIME RAILS_ENV"

{
  printf '%b' "$FROM_DOTENV"
  cat /tmp/grep-raw 2>/dev/null
} | sort -u | while read -r v; do
  [ -z "$v" ] && continue
  case " $RESERVED " in
    *" $v "*) continue ;;
  esac
  case "$v" in
    KUBERNETES_*) continue ;;
  esac
  echo "$v"
done > /tmp/detected.txt

mkdir -p /workspace/.kuso
jq -R -s -c 'split("\n") | map(select(length > 0))' < /tmp/detected.txt > /workspace/.kuso/detected-env.json

echo "KUSO_ENV_DETECT_BEGIN"
cat /workspace/.kuso/detected-env.json
echo
echo "KUSO_ENV_DETECT_END"
echo "env-detect: $(jq -r 'length' /workspace/.kuso/detected-env.json) candidate vars"
`

	return corev1.Container{
		Name:            "env-detect",
		Image:           image,
		SecurityContext: dropAllCapsNonRoot(1000, 1000),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{script},
		Env: []corev1.EnvVar{
			{Name: "REPO_PATH", Value: path},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
		},
	}
}

// renderNixpacksPlanContainer mirrors the chart's nixpacks-plan init.
// The script is preserved verbatim from the chart; we only template
// in the repo path via env. /nix is symlinked to /cache/nix when a
// cache PVC is attached for the warm-store perf win.
func renderNixpacksPlanContainer(b *kube.KusoBuild) corev1.Container {
	image := defaultNixpacksImage + ":" + defaultNixpacksVersion
	path := repoPath(b)
	mounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
	}
	if hasCache(b) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: "cache", MountPath: "/cache",
		})
	}
	useCache := "false"
	if hasCache(b) {
		useCache = "true"
	}
	script := `set -e
if [ "$USE_CACHE" = "true" ]; then
  mkdir -p /cache/nix
  if [ ! -L /nix ] && [ ! -d /nix ]; then
    ln -sf /cache/nix /nix
  elif [ -d /nix ] && [ ! -L /nix ]; then
    cp -an /nix/. /cache/nix/ 2>/dev/null || true
    rm -rf /nix
    ln -sf /cache/nix /nix
  fi
fi
SRC="/workspace/src/$REPO_PATH"
cd "$SRC"

EXTRA_ENVS=""
add_env() {
  EXTRA_ENVS="${EXTRA_ENVS}${EXTRA_ENVS:+ }$1"
  echo "  + $1"
}

echo "detecting project toolchain hints"

if [ -f go.mod ]; then
  MOD_GO=$(awk '/^go [0-9]/ {print $2; exit}' go.mod)
  if [ -n "$MOD_GO" ]; then
    case "$MOD_GO" in
      *.*.*) add_env "GOTOOLCHAIN=go${MOD_GO}+auto" ;;
      *.*)   add_env "GOTOOLCHAIN=go${MOD_GO}.0+auto" ;;
    esac
  fi
  add_env "GOFLAGS=-mod=mod"
fi

if [ -f .nvmrc ]; then
  NODE_V=$(tr -d 'v[:space:]' < .nvmrc | head -c 16)
  [ -n "$NODE_V" ] && add_env "NODE_VERSION=${NODE_V}"
elif [ -f package.json ]; then
  NODE_V=$(grep -oE '"node"[[:space:]]*:[[:space:]]*"[^"]*"' package.json \
    | head -1 \
    | grep -oE '[0-9]+(\.[0-9]+)*' | head -1)
  [ -n "$NODE_V" ] && add_env "NODE_VERSION=${NODE_V}"
fi

if [ -f .python-version ]; then
  PY_V=$(head -1 .python-version | tr -d '[:space:]')
  [ -n "$PY_V" ] && add_env "PYTHON_VERSION=${PY_V}"
elif [ -f pyproject.toml ]; then
  PY_V=$(grep -oE 'requires-python[[:space:]]*=[[:space:]]*"[^"]*"' pyproject.toml \
    | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)
  [ -n "$PY_V" ] && add_env "PYTHON_VERSION=${PY_V}"
fi

if [ -f .ruby-version ]; then
  RB_V=$(head -1 .ruby-version | tr -d '[:space:]')
  [ -n "$RB_V" ] && add_env "RUBY_VERSION=${RB_V}"
elif [ -f Gemfile ]; then
  RB_V=$(grep -oE "^[[:space:]]*ruby[[:space:]]*['\"][^'\"]*['\"]" Gemfile \
    | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)
  [ -n "$RB_V" ] && add_env "RUBY_VERSION=${RB_V}"
fi

if [ -f .sdkmanrc ]; then
  JV=$(grep -oE 'java=[0-9]+(\.[0-9]+)*' .sdkmanrc | head -1 | cut -d= -f2)
  [ -n "$JV" ] && add_env "JDK_VERSION=${JV}"
fi

echo "running nixpacks build --out ."
NIXPACKS_ENV_FLAGS=""
for env_pair in $EXTRA_ENVS; do
  NIXPACKS_ENV_FLAGS="$NIXPACKS_ENV_FLAGS --env $env_pair"
done
# shellcheck disable=SC2086
nixpacks build . --out . $NIXPACKS_ENV_FLAGS

ENV_BLOCK=""
for env_pair in $EXTRA_ENVS; do
  ENV_BLOCK="${ENV_BLOCK}ENV ${env_pair}\n"
done
if [ -n "$ENV_BLOCK" ]; then
  awk -v block="$ENV_BLOCK" '
    BEGIN { inserted = 0 }
    /^FROM / && !inserted { print; printf "%s", block; inserted = 1; next }
    { print }
  ' .nixpacks/Dockerfile > .nixpacks/Dockerfile.patched
  mv .nixpacks/Dockerfile.patched .nixpacks/Dockerfile
fi

echo "--- generated Dockerfile ---"
cat .nixpacks/Dockerfile
`

	return corev1.Container{
		Name:            "nixpacks-plan",
		Image:           image,
		SecurityContext: dropAllCapsRootAllowed(),
		Command:         []string{"/bin/bash", "-c"},
		Args:            []string{script},
		Env: []corev1.EnvVar{
			{Name: "REPO_PATH", Value: path},
			{Name: "USE_CACHE", Value: useCache},
		},
		VolumeMounts: mounts,
	}
}

// renderStaticPlanContainer runs the optional buildCmd in a builder
// sandbox + synthesises a tiny nginx Dockerfile. The static spec is
// optional; defaults apply when nil. buildCmd is a free-form user
// shell, kept as-is (the user owns their own build container).
func renderStaticPlanContainer(b *kube.KusoBuild) corev1.Container {
	builder := defaultStaticBuilderImage
	runtime := defaultStaticRuntimeImage
	buildCmd := ""
	outputDir := "."
	if b != nil && b.Spec.Static != nil {
		if b.Spec.Static.BuilderImage != "" {
			builder = b.Spec.Static.BuilderImage
		}
		if b.Spec.Static.RuntimeImage != "" {
			runtime = b.Spec.Static.RuntimeImage
		}
		buildCmd = b.Spec.Static.BuildCmd
		if b.Spec.Static.OutputDir != "" {
			outputDir = b.Spec.Static.OutputDir
		}
	}

	// We pass buildCmd via env to avoid shell-injection via templated
	// substitution. The user is supposed to set it to a build command;
	// running it via `sh -c "$BUILD_CMD"` evaluates one shell context
	// regardless of the value's content.
	build := `set -e
if [ -n "$BUILD_CMD" ]; then
  echo "running build: $BUILD_CMD"
  sh -c "$BUILD_CMD"
else
  echo "no buildCmd configured; using existing files in $OUTPUT_DIR"
fi
if [ ! -d "$OUTPUT_DIR" ]; then
  echo "ERROR: outputDir $OUTPUT_DIR does not exist after build"
  exit 1
fi
if [ -z "$(ls -A "$OUTPUT_DIR")" ]; then
  echo "ERROR: outputDir $OUTPUT_DIR is empty"
  exit 1
fi
cat > .kuso-static.Dockerfile <<EOF
FROM $RUNTIME_IMAGE
COPY $OUTPUT_DIR /usr/share/nginx/html
EOF
echo "--- generated Dockerfile ---"
cat .kuso-static.Dockerfile
`

	mounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
	}
	if hasCache(b) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: "cache", MountPath: "/root/.npm", SubPath: "deps/npm",
		})
	}

	return corev1.Container{
		Name:            "static-plan",
		Image:           builder,
		SecurityContext: dropAllCapsRootAllowed(),
		WorkingDir:      "/workspace/src/" + repoPath(b),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{build},
		Env: []corev1.EnvVar{
			{Name: "BUILD_CMD", Value: buildCmd},
			{Name: "OUTPUT_DIR", Value: outputDir},
			{Name: "RUNTIME_IMAGE", Value: runtime},
		},
		VolumeMounts: mounts,
	}
}

// renderBuildpacksContainer is the CNB lifecycle creator. Runs as
// 1000:1000 (lifecycle contract). Optional GITHUB_INSTALLATION_TOKEN
// for private buildpacks / git-hosted deps.
func renderBuildpacksContainer(buildName string, b *kube.KusoBuild, res corev1.ResourceRequirements) corev1.Container {
	lifecycle := defaultBuildpacksImage
	builder := defaultBuildpacksBuilder
	if b != nil && b.Spec.Buildpacks != nil {
		if b.Spec.Buildpacks.LifecycleImage != "" {
			lifecycle = b.Spec.Buildpacks.LifecycleImage
		}
		if b.Spec.Buildpacks.BuilderImage != "" {
			builder = b.Spec.Buildpacks.BuilderImage
		}
	}
	imageRef := fmt.Sprintf("%s:%s", b.Spec.Image.Repository, b.Spec.Image.Tag)
	envs := []corev1.EnvVar{
		{Name: "CNB_BUILDER_IMAGE", Value: builder},
	}
	if hasAuthSecret(b) {
		envs = append(envs, corev1.EnvVar{
			Name: "CNB_REGISTRY_AUTH",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: b.Spec.Auth.SecretName},
					Key:                  "cnb_registry_auth",
				},
			},
		})
	}
	if b != nil && b.Spec.GithubInstallationID > 0 {
		envs = append(envs, corev1.EnvVar{
			Name: "GITHUB_INSTALLATION_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: buildName + "-token"},
					Key:                  "token",
				},
			},
		})
	}
	return corev1.Container{
		Name:            "buildpacks",
		Image:           lifecycle,
		SecurityContext: dropAllCapsNonRoot(1000, 1000),
		Command: []string{
			"/cnb/lifecycle/creator",
			"-app=/workspace/src/" + repoPath(b),
			"-log-level=info",
			"-no-color",
			"-skip-restore",
			imageRef,
		},
		Env: envs,
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/workspace"},
			{Name: "layers", MountPath: "/layers"},
			{Name: "cnb-cache", MountPath: "/cache"},
		},
		Resources: res,
	}
}

// renderBuildkitContainer is the buildkit thin-client. Talks to the
// long-lived kuso-buildkitd Deployment over TCP. The actual build
// happens in the daemon; this container just uploads the workspace
// context and streams progress back.
func renderBuildkitContainer(b *kube.KusoBuild, strategy string, res corev1.ResourceRequirements) corev1.Container {
	path := repoPath(b)
	dockerfile := "Dockerfile"
	switch strategy {
	case "nixpacks":
		dockerfile = ".nixpacks/Dockerfile"
	case "static":
		dockerfile = ".kuso-static.Dockerfile"
	}
	image := fmt.Sprintf("%s:%s", b.Spec.Image.Repository, b.Spec.Image.Tag)
	cache := fmt.Sprintf("%s:buildcache", b.Spec.Image.Repository)

	script := `CTX="/workspace/src/$REPO_PATH"
DF=$DOCKERFILE
IMAGE=$IMAGE_REF
CACHE=$CACHE_REF
BUILDKIT_HOST=$BUILDKIT_ADDR

echo "==> buildkit: daemon=$BUILDKIT_HOST"
echo "==> buildkit: image=$IMAGE cache=$CACHE df=$DF ctx=$CTX"

for i in $(seq 1 30); do
  if buildctl --addr "$BUILDKIT_HOST" debug workers >/dev/null 2>&1; then
    break
  fi
  echo "==> waiting for buildkitd ($i/30)..."
  sleep 1
done

exec buildctl \
  --addr "$BUILDKIT_HOST" \
  build \
  --frontend dockerfile.v0 \
  --local context="$CTX" \
  --local dockerfile="$CTX" \
  --opt filename="$DF" \
  --output type=image,name="$IMAGE",push=true,registry.insecure=true \
  --export-cache type=registry,ref="$CACHE",mode=max,registry.insecure=true \
  --import-cache type=registry,ref="$CACHE",registry.insecure=true \
  --progress plain
`

	envs := []corev1.EnvVar{
		{Name: "HOME", Value: "/tmp"},
		{Name: "REPO_PATH", Value: path},
		{Name: "DOCKERFILE", Value: dockerfile},
		{Name: "IMAGE_REF", Value: image},
		{Name: "CACHE_REF", Value: cache},
		{Name: "BUILDKIT_ADDR", Value: defaultBuildkitHost},
	}
	if hasAuthSecret(b) {
		envs = append(envs, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/tmp/.docker"})
	}

	mounts := []corev1.VolumeMount{
		{Name: "workspace", MountPath: "/workspace"},
	}
	if hasAuthSecret(b) {
		mounts = append(mounts, corev1.VolumeMount{
			Name: "docker-config", MountPath: "/tmp/.docker", ReadOnly: true,
		})
	}
	// nixpacks /cache mount survives via the same emptyDir-shared
	// workspace; legacy per-language subPaths still mount when a
	// cache PVC is attached.
	if hasCache(b) {
		if strategy == "nixpacks" {
			mounts = append(mounts, corev1.VolumeMount{
				Name: "cache", MountPath: "/cache",
			})
		}
		mounts = append(mounts,
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.npm", SubPath: "deps/npm"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/go/pkg/mod", SubPath: "deps/go-mod"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.cache/go-build", SubPath: "deps/go-build"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.cache/pip", SubPath: "deps/pip"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.cargo/registry", SubPath: "deps/cargo"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.gradle", SubPath: "deps/gradle"},
			corev1.VolumeMount{Name: "cache", MountPath: "/tmp/.m2", SubPath: "deps/m2"},
		)
	}

	return corev1.Container{
		Name:            "buildkit",
		Image:           defaultBuildkitImage,
		SecurityContext: dropAllCapsNonRoot(1000, 1000),
		Command:         []string{"/bin/sh", "-ec"},
		Args:            []string{script},
		Env:             envs,
		VolumeMounts:    mounts,
		Resources:       res,
	}
}

// shellQuote single-quotes a string for safe embedding in a /bin/sh
// command line. Embedded single quotes get the standard '\'' escape.
// The kuso-server boundary validates repo URLs, branches, and refs
// before stamping the CR — this is defense-in-depth so a malformed
// or hostile CR (kubectl apply by an admin) can't break out of the
// argument quoting and run arbitrary commands as the clone init.
func shellQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
