package buildcontroller

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/builds"
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

// renderCloneContainer is the always-on git clone init. Private repos read
// a clone token (KUSO_GIT_TOKEN) from the chart-rendered <build>-token
// Secret. For GitHub the kuso-server build poller mints a short-lived App
// installation token; for GitLab it copies the service's stored deploy /
// project-access token. The HTTP userinfo differs per provider: GitHub uses
// "x-access-token:<tok>", GitLab uses "oauth2:<tok>".
func renderCloneContainer(buildName string, b *kube.KusoBuild) corev1.Container {
	repoURL := ""
	branch := "main"
	ref := ""
	var repoRef *kube.KusoRepoRef
	if b != nil {
		repoRef = b.Spec.Repo
		if repoRef != nil {
			repoURL = repoRef.URL
		}
		if b.Spec.Branch != "" {
			branch = b.Spec.Branch
		}
		ref = b.Spec.Ref
	}
	// Private = a token is needed to clone: a GitHub App installation OR a
	// GitLab repo with a stored token secret.
	provider := kube.RepoProviderForRef(repoRef)
	private := buildNeedsCloneToken(b)

	// Build the clone script. Values are quoted via shellQuote so a
	// malicious repo URL or branch (validated upstream but defense-in-
	// depth) can't break out.
	cloneCmd := ""
	if private {
		// Auth userinfo by provider. GitHub: x-access-token:<tok>.
		// GitLab: oauth2:<tok> (works for deploy/project/personal tokens).
		userinfo := "x-access-token"
		if provider == kube.ProviderGitLab {
			userinfo = "oauth2"
		}
		cloneCmd = fmt.Sprintf(`
if [ -z "$KUSO_GIT_TOKEN" ]; then
  echo "ERROR: KUSO_GIT_TOKEN must be set for private repos"
  exit 1
fi
URL=%s
BRANCH=%s
git clone --depth 1 --branch "$BRANCH" \
  "https://%s:${KUSO_GIT_TOKEN}@$(echo "$URL" | sed -E 's|^https?://||')" \
  /workspace/src
`, shellQuote(repoURL), shellQuote(branch), userinfo)
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
		c.Env = []corev1.EnvVar{gitTokenEnvVar(buildName)}
	}
	return c
}

// buildNeedsCloneToken reports whether a build must clone with a token: a
// GitHub App installation OR a GitLab repo with a stored token secret.
func buildNeedsCloneToken(b *kube.KusoBuild) bool {
	if b == nil {
		return false
	}
	if b.Spec.GithubInstallationID > 0 {
		return true
	}
	return kube.RepoProviderForRef(b.Spec.Repo) == kube.ProviderGitLab &&
		b.Spec.Repo != nil && b.Spec.Repo.TokenSecret != ""
}

// gitTokenEnvVar mounts the clone token from the <build>-token Secret as
// KUSO_GIT_TOKEN. Provider-agnostic: the poller writes the right token
// (GitHub App installation token or the GitLab stored token) under the
// same "token" key.
func gitTokenEnvVar(buildName string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: "KUSO_GIT_TOKEN",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: buildName + "-token"},
				Key:                  "token",
			},
		},
	}
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

# Runtime-only / kuso-managed keys that must never be injected into the
# build (NODE_ENV=production makes pnpm/npm skip devDeps → install fails).
# MUST mirror builds.reservedBuildEnvKeys (server) + the detect-script's
# RESERVED. Used by the KUSO_BUILDENV_KEYS guards below as defense-in-depth.
RESERVED="PORT HOSTNAME HOME PATH USER PWD SHELL TERM LANG LC_ALL LC_CTYPE NODE_ENV NODE_OPTIONS NODE_VERSION NPM_CONFIG_LOGLEVEL DEBIAN_FRONTEND DEBUG CI VERCEL_ENV NEXT_RUNTIME RAILS_ENV"

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
# Accumulate the --env flags as POSITIONAL PARAMS (set --) rather than a
# space-joined string. A build-env VALUE containing spaces (e.g.
# RESEND_FROM="Name <addr>") would word-split out of an unquoted string and
# nixpacks would parse the trailing token as a stray argument (exit 2). With
# set -- / "$@", each --env KEY=VALUE stays exactly two argv tokens and the
# VALUE survives intact regardless of spaces/metachars.
set --
# Toolchain hints (EXTRA_ENVS) are kuso-generated KEY=VALUE pairs with no
# spaces, but route them through the same safe path for uniformity.
for env_pair in $EXTRA_ENVS; do
  set -- "$@" --env "$env_pair"
done
# Pass the service's build-time env to nixpacks two ways:
#  1. --env KEY=VALUE so the value is available to build commands, AND
#  2. for NIXPACKS_* keys, EXPORT the real var into nixpacks' own process
#     env — nixpacks reads NIXPACKS_NODE_VERSION/PYTHON_VERSION/etc. from
#     its environment to pick the toolchain at plan time; a --env flag
#     alone is not honored for toolchain selection in nixpacks v1.41.
for k in $KUSO_BUILDENV_KEYS; do
  # Defense-in-depth: never pass a runtime-only / kuso-managed key into the
  # build, even if one somehow reached spec.buildEnv (the server already
  # filters via builds.reservedBuildEnvKeys; keep the two in lockstep).
  # NODE_ENV=production here makes pnpm/npm skip devDeps and breaks installs.
  case " $RESERVED " in
    *" $k "*) continue ;;
  esac
  kv="$(printenv "KUSO_BE_${k}")"
  set -- "$@" --env "${k}=${kv}"
  case "$k" in
    NIXPACKS_*) export "${k}=${kv}"; echo "  export ${k}" ;;
  esac
done
nixpacks build . --out . "$@"

# ENV lines to inject after the FROM line. Toolchain hints (EXTRA_ENVS,
# KEY=VALUE form) first; then the service's build-time env. The latter
# arrives as container env vars KUSO_BE_<KEY> (kubelet handles all value
# escaping — values never pass through shell parsing, so no injection
# risk) with the key list in KUSO_BUILDENV_KEYS. We use Dockerfile's
# space-form (ENV KEY VALUE) so values with '='/':'/'/'/spaces are fine.
# KUSO_BUILDENV_KEYS carries LITERAL vars only: these ENV lines are
# permanent image layers, so secret-sourced vars are never present here
# (see buildEnvSecretContainerVars).
ENV_BLOCK=""
for env_pair in $EXTRA_ENVS; do
  k="${env_pair%%=*}"; v="${env_pair#*=}"
  ENV_BLOCK="${ENV_BLOCK}ENV ${k} ${v}\n"
done
for k in $KUSO_BUILDENV_KEYS; do
  # Same RESERVED guard as the --env loop above (defense-in-depth).
  case " $RESERVED " in
    *" $k "*) continue ;;
  esac
  # value from KUSO_BE_<key>; printf the literal so no re-evaluation.
  v="$(printenv "KUSO_BE_${k}")"
  ENV_BLOCK="${ENV_BLOCK}ENV ${k} ${v}\n"
done
if [ -n "$ENV_BLOCK" ]; then
  awk -v block="$ENV_BLOCK" '
    BEGIN { inserted = 0 }
    /^FROM / && !inserted { print; printf "%s", block; inserted = 1; next }
    { print }
  ' .nixpacks/Dockerfile > .nixpacks/Dockerfile.patched
  mv .nixpacks/Dockerfile.patched .nixpacks/Dockerfile
fi

# Print FROM + injected ENV KEYS only — never values (build-time env may
# carry secrets, and build logs are user-visible).
echo "--- Dockerfile FROM + injected ENV keys ---"
grep -E '^FROM ' .nixpacks/Dockerfile | head -3
grep -E '^ENV ' .nixpacks/Dockerfile | awk '{print "ENV " $2}' | head -80
`

	env := []corev1.EnvVar{
		{Name: "REPO_PATH", Value: path},
		{Name: "USE_CACHE", Value: useCache},
	}
	// LITERAL build env only. Secret-sourced vars are deliberately absent
	// from this pod: everything handed to the nixpacks flow ends up as an
	// `ENV` line in the generated Dockerfile — a permanent, registry-
	// readable image layer — and the generated RUN steps can't opt into
	// buildkit secret mounts the way a user-authored Dockerfile can. A
	// build step that needs a secret at build time isn't supported under
	// nixpacks; runtime pods still get it via the deployment's envFrom.
	env = append(env, buildEnvContainerVars(b)...)
	return corev1.Container{
		Name:            "nixpacks-plan",
		Image:           image,
		SecurityContext: dropAllCapsRootAllowed(),
		Command:         []string{"/bin/bash", "-c"},
		Args:            []string{script},
		Env:             env,
		VolumeMounts:    mounts,
	}
}

// envKeyRE is the POSIX env-var identifier. buildEnv keys are interpolated
// into both a shell var name (KUSO_BE_<key>) and an `ENV <key>` Dockerfile
// line, so a non-identifier key would be a shell-injection / malformed-ENV
// vector. The server already validates keys (builds.buildEnvFromVars), but we
// re-validate here as defense-in-depth at the render boundary.
var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// buildEnvContainerVars turns b.Spec.BuildEnv's LITERAL entries into
// container env vars: KUSO_BUILDENV_KEYS (space-separated key list) + one
// KUSO_BE_<KEY> per entry. Values flow as kubelet-managed env (no shell
// escaping needed). Keys failing the identifier check are dropped.
//
// SECURITY: kuso-secret-ref:// values are EXCLUDED here — everything in
// KUSO_BUILDENV_KEYS ends up persisted in the published image (build-arg
// values are recoverable from the image config/history; nixpacks bakes ENV
// lines), so secret-sourced vars must only ever travel through
// buildEnvSecretContainerVars. A prefix-carrying value that fails to parse
// is dropped entirely, never demoted to a literal.
func buildEnvContainerVars(b *kube.KusoBuild) []corev1.EnvVar {
	if b == nil || len(b.Spec.BuildEnv) == 0 {
		return nil
	}
	var out []corev1.EnvVar
	var keys []string
	for _, k := range sortedBuildEnvKeys(b) {
		v := b.Spec.BuildEnv[k]
		if builds.IsBuildEnvSecretRef(v) {
			continue
		}
		keys = append(keys, k)
		out = append(out, corev1.EnvVar{Name: "KUSO_BE_" + k, Value: v})
	}
	if len(keys) == 0 {
		return nil
	}
	out = append(out, corev1.EnvVar{Name: "KUSO_BUILDENV_KEYS", Value: strings.Join(keys, " ")})
	return out
}

// buildEnvSecretContainerVars turns b.Spec.BuildEnv's kuso-secret-ref://
// entries into secretKeyRef env mounts (KUSO_BE_<KEY>, value resolved by the
// kubelet at pod start — plaintext never touches the CR or the rendered Job)
// plus the KUSO_BUILDENV_SECRET_KEYS list the buildkit script forwards as
// `--secret id=<KEY>,env=KUSO_BE_<KEY>`. Malformed refs are dropped.
func buildEnvSecretContainerVars(b *kube.KusoBuild) []corev1.EnvVar {
	if b == nil || len(b.Spec.BuildEnv) == 0 {
		return nil
	}
	var out []corev1.EnvVar
	var keys []string
	for _, k := range sortedBuildEnvKeys(b) {
		secret, key, ok := builds.ParseBuildEnvSecretRef(b.Spec.BuildEnv[k])
		if !ok {
			continue
		}
		keys = append(keys, k)
		out = append(out, corev1.EnvVar{
			Name: "KUSO_BE_" + k,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secret},
					Key:                  key,
					// Optional: a secret deleted between trigger and Job
					// start (builds can queue) degrades to "var omitted" —
					// same as the trigger-time omit-on-unresolvable rule —
					// instead of wedging the pod in CreateContainerConfigError.
					Optional: ptrTrue(),
				},
			},
		})
	}
	if len(keys) == 0 {
		return nil
	}
	out = append(out, corev1.EnvVar{Name: "KUSO_BUILDENV_SECRET_KEYS", Value: strings.Join(keys, " ")})
	return out
}

// buildEnvWithheldKeyNames returns the space-joined NAMES of secret-sourced
// buildEnv keys (sorted; never values, never the refs themselves). Used to
// surface a WARNING in the build log for strategies that withhold secrets
// from the pod entirely (nixpacks/static) — the withholding itself is
// correct (see buildEnvSecretContainerVars), but silently building without
// a var the user configured is a debugging trap without the signal.
func buildEnvWithheldKeyNames(b *kube.KusoBuild) string {
	if b == nil || len(b.Spec.BuildEnv) == 0 {
		return ""
	}
	var keys []string
	for _, k := range sortedBuildEnvKeys(b) {
		if builds.IsBuildEnvSecretRef(b.Spec.BuildEnv[k]) {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, " ")
}

// sortedBuildEnvKeys returns the identifier-valid buildEnv keys in stable
// order (deterministic rendering; tests). The identifier check is
// defense-in-depth at the render boundary — keys are interpolated into shell
// var names and Dockerfile ENV lines (server already validates via
// builds.buildEnvFromVars).
func sortedBuildEnvKeys(b *kube.KusoBuild) []string {
	names := make([]string, 0, len(b.Spec.BuildEnv))
	for k := range b.Spec.BuildEnv {
		if envKeyRE.MatchString(k) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
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
	if buildNeedsCloneToken(b) {
		envs = append(envs, gitTokenEnvVar(buildName))
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
	// dockerfile strategy honors the per-service override (monorepo
	// services building from e.g. "Dockerfile.dev"); nixpacks/static use
	// their generated Dockerfiles. Mirrors the helm-chart renderer, which
	// resolves `.Values.dockerfile | default "Dockerfile"`.
	dockerfile := "Dockerfile"
	switch strategy {
	case "nixpacks":
		dockerfile = ".nixpacks/Dockerfile"
	case "static":
		dockerfile = ".kuso-static.Dockerfile"
	default:
		if df := strings.TrimSpace(b.Spec.Dockerfile); df != "" {
			dockerfile = df
		}
	}
	image := fmt.Sprintf("%s:%s", b.Spec.Image.Repository, b.Spec.Image.Tag)
	cache := fmt.Sprintf("%s:buildcache", b.Spec.Image.Repository)

	// DRY_RUN=1 flips buildkit to a parse + compile mode that doesn't
	// push to the registry and doesn't update the cache. The build
	// pod still goes through the full Dockerfile evaluation — base
	// image pull, every COPY/RUN/etc — so a broken stage surfaces
	// the same error as a real build. The poller checks spec.dryRun
	// on terminal transition and skips env promotion.
	script := `CTX="/workspace/src/$REPO_PATH"
DF=$DOCKERFILE
IMAGE=$IMAGE_REF
CACHE=$CACHE_REF
BUILDKIT_HOST=$BUILDKIT_ADDR

# Runtime-only / kuso-managed keys never passed to the build (NODE_ENV=
# production makes the install skip devDeps). Mirrors builds.reservedBuildEnvKeys.
RESERVED="PORT HOSTNAME HOME PATH USER PWD SHELL TERM LANG LC_ALL LC_CTYPE NODE_ENV NODE_OPTIONS NODE_VERSION NPM_CONFIG_LOGLEVEL DEBIAN_FRONTEND DEBUG CI VERCEL_ENV NEXT_RUNTIME RAILS_ENV"

# Loud, up-front signal that secret-sourced build env no longer flows as
# build-args / baked ENV (the security fix that stops credentials persisting
# in registry-readable image layers). Without this, a Dockerfile still using
# 'ARG DATABASE_URL' builds "successfully" with an empty value and nothing in
# the log points at the cause. Key NAMES only — never values.
if [ -n "${KUSO_BUILDENV_SECRET_KEYS:-}" ]; then
  echo "WARNING: secret-sourced build env vars are no longer passed as build-args: ${KUSO_BUILDENV_SECRET_KEYS}"
  echo "WARNING: a Dockerfile 'ARG <KEY>' for any of these keys now receives an EMPTY value."
  echo "WARNING: migrate to a BuildKit secret mount instead: RUN --mount=type=secret,id=<KEY> ... (value readable at /run/secrets/<KEY> for that RUN only)."
fi
if [ -n "${KUSO_BUILDENV_WITHHELD_KEYS:-}" ]; then
  echo "WARNING: secret-sourced build env vars are WITHHELD from this build: ${KUSO_BUILDENV_WITHHELD_KEYS}"
  echo "WARNING: nixpacks/static builds bake all build-time env into permanent image layers, so secret values are never available at build time."
  echo "WARNING: consume these vars at RUNTIME instead — the deployed pods still receive them via the environment's secret mounts."
fi

# Build-args from the service's build-time env (KUSO_BE_<KEY> container vars).
# Each becomes --opt build-arg:KEY=VALUE; the Dockerfile consumes the ones it
# declares ARG for, the rest are ignored by buildkit. Accumulate into the
# positional params via set -- so a VALUE containing spaces survives as one
# argument (a plain string + word-splitting would break it). Values come from
# the environment (printenv), never interpolated into the script, so no
# shell-injection regardless of value contents.
set --
for k in $KUSO_BUILDENV_KEYS; do
  case " $RESERVED " in
    *" $k "*) continue ;;
  esac
  set -- "$@" --opt "build-arg:${k}=$(printenv "KUSO_BE_${k}")"
done

# Secret-sourced build env (KUSO_BUILDENV_SECRET_KEYS) must NEVER flow as a
# build-arg — buildkit records consumed build-arg values in the pushed
# image's config/history, so registry read access would recover live addon
# credentials. Forwarded as buildkit secrets instead (value read client-side
# from the KUSO_BE_<KEY> env the kubelet mounted): the Dockerfile opts in
# with "RUN --mount=type=secret,id=<KEY>" (file at /run/secrets/<KEY>, or
# ",env=<KEY>" on frontends >= 1.10) and the value exists only for that RUN
# instruction — never in a layer. Unrequested secret ids are ignored.
for k in $KUSO_BUILDENV_SECRET_KEYS; do
  case " $RESERVED " in
    *" $k "*) continue ;;
  esac
  set -- "$@" --secret "id=${k},env=KUSO_BE_${k}"
done

echo "==> buildkit: daemon=$BUILDKIT_HOST"
echo "==> buildkit: image=$IMAGE cache=$CACHE df=$DF ctx=$CTX dryRun=${DRY_RUN:-0}"
echo "==> buildkit: build-arg keys=${KUSO_BUILDENV_KEYS:-<none>}"
echo "==> buildkit: secret ids=${KUSO_BUILDENV_SECRET_KEYS:-<none>}"

for i in $(seq 1 30); do
  if buildctl --addr "$BUILDKIT_HOST" debug workers >/dev/null 2>&1; then
    break
  fi
  echo "==> waiting for buildkitd ($i/30)..."
  sleep 1
done

if [ "${DRY_RUN:-0}" = "1" ]; then
  # Dry-run: parse + compile, no push, no cache mutation. The image
  # is discarded after the buildkit run completes.
  exec buildctl \
    --addr "$BUILDKIT_HOST" \
    build \
    --frontend dockerfile.v0 \
    --local context="$CTX" \
    --local dockerfile="$CTX" \
    --opt filename="$DF" \
    "$@" \
    --output type=image,name="$IMAGE",push=false,registry.insecure=true \
    --import-cache type=registry,ref="$CACHE",registry.insecure=true \
    --progress plain
fi

exec buildctl \
  --addr "$BUILDKIT_HOST" \
  build \
  --frontend dockerfile.v0 \
  --local context="$CTX" \
  --local dockerfile="$CTX" \
  --opt filename="$DF" \
  "$@" \
  --output type=image,name="$IMAGE",push=true,registry.insecure=true \
  --export-cache type=registry,ref="$CACHE",mode=max,registry.insecure=true \
  --import-cache type=registry,ref="$CACHE",registry.insecure=true \
  --progress plain
`

	dryRun := "0"
	if b.Spec.DryRun {
		dryRun = "1"
	}
	envs := []corev1.EnvVar{
		{Name: "HOME", Value: "/tmp"},
		{Name: "REPO_PATH", Value: path},
		{Name: "DOCKERFILE", Value: dockerfile},
		{Name: "IMAGE_REF", Value: image},
		{Name: "CACHE_REF", Value: cache},
		{Name: "BUILDKIT_ADDR", Value: defaultBuildkitHost},
		{Name: "DRY_RUN", Value: dryRun},
	}
	if hasAuthSecret(b) {
		envs = append(envs, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/tmp/.docker"})
	}
	// Build-time env for RAW Dockerfile builds. nixpacks/static generate a
	// Dockerfile kuso edits to inject ENV lines; a raw Dockerfile we don't
	// own, so we pass each LITERAL buildEnv key as a `--opt build-arg`
	// instead. The Dockerfile opts in by declaring `ARG <KEY>`. A build-arg
	// the Dockerfile doesn't declare is a harmless no-op in buildkit, so
	// passing all keys is safe. Values arrive as KUSO_BE_<KEY> container
	// env (kubelet-escaped; never shell-parsed) with the key list in
	// KUSO_BUILDENV_KEYS — same mechanism as the nixpacks-plan container.
	envs = append(envs, buildEnvContainerVars(b)...)
	// Secret-sourced build env is forwarded ONLY for user-authored
	// Dockerfiles, and only as buildkit secret mounts — a consumed build-arg
	// persists in the pushed image's config/history, which is exactly the
	// credential leak this split exists to close. A Dockerfile that used to
	// read `ARG DATABASE_URL` must migrate to
	// `RUN --mount=type=secret,id=DATABASE_URL ...`. nixpacks/static build
	// kuso-generated Dockerfiles that can't opt into secret mounts, so for
	// those strategies the secret values are withheld from the pod entirely
	// (runtime env still arrives via the deployment's envFrom).
	if strategy == "dockerfile" {
		envs = append(envs, buildEnvSecretContainerVars(b)...)
	} else if withheld := buildEnvWithheldKeyNames(b); withheld != "" {
		// nixpacks/static withhold secret-sourced vars from the pod
		// entirely; hand the script the key NAMES (never values or refs)
		// so the build log carries a visible WARNING explaining why a
		// build step that reads one of these sees nothing.
		envs = append(envs, corev1.EnvVar{Name: "KUSO_BUILDENV_WITHHELD_KEYS", Value: withheld})
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
// command line. Embedded single quotes get the standard '\” escape.
//
// The kuso-server boundary validates repo URLs and branches before
// stamping the CR (builds.ValidateRepoURL / builds.ValidateGitRef) —
// this is defense-in-depth so a malformed or hostile CR (kubectl apply
// by an admin, bypassing the API) can't break out of the argument
// quoting and run arbitrary commands as the clone init.
//
// NOTE: that first sentence was aspirational until those validators
// existed — for several releases this function was the ONLY barrier.
// If you add another interpolation site, quote it here AND validate at
// the boundary; neither layer is meant to stand alone.
func shellQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
