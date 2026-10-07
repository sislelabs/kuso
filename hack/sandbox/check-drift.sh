#!/usr/bin/env bash
#
# Fails when deploy/ and hack/sandbox/manifests.sh disagree: a manifest in
# neither list, in both, or a listed name with no file behind it.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
source "${SANDBOX_DIR}/manifests.sh"

fail=0
listed=" ${SANDBOX_APPLIED[*]} ${SANDBOX_SKIPPED[*]} "

for f in "${REPO_ROOT}"/deploy/*.yaml; do
  name="$(basename "$f" .yaml)"
  if [[ "$listed" != *" ${name} "* ]]; then
    echo "deploy/${name}.yaml is not listed in hack/sandbox/manifests.sh (add it to SANDBOX_APPLIED or SANDBOX_SKIPPED)" >&2
    fail=1
  fi
done

for name in "${SANDBOX_APPLIED[@]}" "${SANDBOX_SKIPPED[@]}"; do
  [[ -f "${REPO_ROOT}/deploy/${name}.yaml" ]] || { echo "hack/sandbox/manifests.sh lists ${name}, but deploy/${name}.yaml does not exist" >&2; fail=1; }
done

for name in "${SANDBOX_APPLIED[@]}"; do
  if [[ " ${SANDBOX_SKIPPED[*]} " == *" ${name} "* ]]; then
    echo "${name} is in both SANDBOX_APPLIED and SANDBOX_SKIPPED" >&2
    fail=1
  fi
  # Listed as applied but never passed to bootstrap.sh's manifest().
  grep -qE "manifest ${name}( |\$)" "${SANDBOX_DIR}/bootstrap.sh" \
    || { echo "${name} is in SANDBOX_APPLIED but bootstrap.sh never applies it" >&2; fail=1; }
done

[[ "$fail" == "0" ]] && echo "sandbox manifest lists match deploy/"
exit "$fail"
