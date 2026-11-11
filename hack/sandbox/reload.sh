#!/usr/bin/env bash
#
# Rebuild kuso images from the working tree and restart what uses them.
#   reload.sh            server + operator
#   reload.sh server     server (and the activator, which runs the same image)
#   reload.sh operator
#   reload.sh backup|env-detect|nixpacks   a Job image (pushed to the mirror)
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

node_running || die "the sandbox is not running; run 'make sandbox-up'"

targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(server operator)

for t in "${targets[@]}"; do
  case "$t" in
    server)
      build_image server; import_image server
      node_kubectl -n kuso rollout restart deployment/kuso-server deployment/kuso-activator >/dev/null
      node_kubectl -n kuso rollout status deployment/kuso-server --timeout=300s
      ;;
    operator)
      build_image operator; import_image operator
      node_kubectl -n kuso-operator-system rollout restart deployment/kuso-operator-controller-manager >/dev/null
      node_kubectl -n kuso-operator-system rollout status deployment/kuso-operator-controller-manager --timeout=300s
      ;;
    backup|env-detect|nixpacks)
      build_image "$t"; import_image "$t"; mirror_image "$t"
      ;;
    *) die "unknown target: ${t} (want server, operator, backup, env-detect or nixpacks)" ;;
  esac
done
