package buildcontroller

import (
	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
)

// foreignImageCheckScript is a stopgap for BLOB-R4: the registry has one
// credential that reads every repository, and buildkit forwards it for any
// FROM / COPY --from / RUN --mount=from that names the in-cluster registry,
// so a Dockerfile in project A could pull project B's images. Until token
// auth scopes the build credential to <project>/*, the build container
// scans the Dockerfile and the build-arg values for an in-cluster registry
// ref whose first path segment is not this project.
//
// It is a lint, not a boundary: a ref assembled from several ARGs, or the
// registry's ClusterIP, gets past it. Preview builds (PR code) refuse;
// other builds warn, so a project that deliberately builds FROM a shared
// base project keeps deploying.
//
// Shell builtins plus printenv only; it runs in moby/buildkit's busybox sh.
const foreignImageCheckScript = `
kuso_foreign_refs() {
  set -f
  while IFS= read -r _l || [ -n "$_l" ]; do
    for _t in $_l; do
      case "$_t" in *kuso-registry*) ;; *) continue ;; esac
      _pre=${_t%%kuso-registry*}
      case "$_pre" in ""|*=|*\"|*\'|*/) ;; *) continue ;; esac
      _rest=${_t#*kuso-registry}
      case "$_rest" in [:./]*/*) ;; *) continue ;; esac
      _path=${_rest#*/}
      [ "${_path%%/*}" = "$KUSO_BUILD_PROJECT" ] && continue
      printf '%s\n' "$_t"
    done
  done
  set +f
}
# Build-arg values can carry credentials: report the key, never the value.
KUSO_FOREIGN=$(
  if [ -f "$CTX/$DF" ]; then kuso_foreign_refs < "$CTX/$DF"; fi
  for _k in $KUSO_BUILDENV_KEYS; do
    if [ -n "$( (printenv "KUSO_BE_${_k}" || true) | kuso_foreign_refs)" ]; then echo "build env ${_k}"; fi
  done
  for _k in $KUSO_BUILDARG_KEYS; do
    if [ -n "$( (printenv "KUSO_BA_${_k}" || true) | kuso_foreign_refs)" ]; then echo "build arg ${_k}"; fi
  done
  true
)
if [ -n "$KUSO_FOREIGN" ] && [ -n "${KUSO_BUILD_PROJECT:-}" ]; then
  echo "WARNING: this build names images of another project on the in-cluster registry; a build may only use its own project's images (${KUSO_BUILD_PROJECT}/...):"
  printf '%s\n' "$KUSO_FOREIGN" | while IFS= read -r _r; do echo "  $_r"; done
  if [ "${FOREIGN_IMAGE_POLICY:-warn}" = "refuse" ]; then
    echo "ERROR: preview builds may not pull another project's images; build the base image in this project or push it to an external registry" >&2
    exit 1
  fi
fi
`

// foreignImagePolicy is "refuse" for preview builds (PR code) and "warn"
// for every other build.
func foreignImagePolicy(b *kube.KusoBuild) string {
	if cacheExport(b) == "0" {
		return "refuse"
	}
	return "warn"
}

func foreignImageCheckEnv(b *kube.KusoBuild) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "KUSO_BUILD_PROJECT", Value: b.Spec.Project},
		{Name: "FOREIGN_IMAGE_POLICY", Value: foreignImagePolicy(b)},
	}
}
