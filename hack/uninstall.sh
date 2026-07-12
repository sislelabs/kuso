#!/usr/bin/env bash
#
# kuso uninstaller — reverses hack/install.sh on a single-host install.
#
# What it removes:
#   - the k3s cluster (via the k3s-uninstall.sh k3s itself drops in), which
#     takes every kuso workload, PVC, and the in-cluster registry with it
#   - /etc/kuso/github-app.{env,pem}  (plaintext GitHub App credentials —
#     these otherwise persist and are silently inherited by a re-install)
#   - the /etc/hosts entry for the in-cluster registry
#   - /etc/rancher/k3s/registries.yaml
#
# What it does NOT touch (data-loss guard): nothing outside the above. If
# you installed onto a PRE-EXISTING k3s (KUSO_SKIP_K3S=1), pass
# --keep-cluster to leave k3s alone and only remove kuso's host files;
# you'll then need to delete the kuso namespaces yourself.
#
# Usage:
#   sudo hack/uninstall.sh [--keep-cluster] [--yes]

set -euo pipefail

KEEP_CLUSTER=0
ASSUME_YES=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep-cluster) KEEP_CLUSTER=1; shift ;;
    --yes|-y)       ASSUME_YES=1; shift ;;
    -h|--help)
      cat <<'USAGE'
kuso uninstaller. Reverses hack/install.sh.

  sudo hack/uninstall.sh            remove k3s + all kuso data + host files
  sudo hack/uninstall.sh --keep-cluster   keep k3s, remove only kuso host files
  sudo hack/uninstall.sh --yes            skip the confirmation prompt

DESTRUCTIVE: without --keep-cluster this deletes the k3s cluster and every
PVC on this host (databases, volumes). There is no undo.
USAGE
      exit 0 ;;
    *) printf 'unknown flag: %s\n' "$1" >&2; exit 2 ;;
  esac
done

log()  { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

if [[ "$(id -u)" -ne 0 ]]; then
  die "must run as root (we touch /etc/rancher, /etc/hosts, /etc/kuso). Try: sudo $0"
fi

if [[ "$KEEP_CLUSTER" -eq 0 && "$ASSUME_YES" -eq 0 ]]; then
  warn "This DELETES the k3s cluster and every kuso PVC on this host (databases, volumes)."
  warn "There is no undo. Back up first: kuso backup + kuso project export-archive per project."
  printf 'Type "delete" to proceed: '
  read -r reply
  [[ "$reply" == "delete" ]] || die "aborted"
fi

if [[ "$KEEP_CLUSTER" -eq 0 ]]; then
  if [[ -x /usr/local/bin/k3s-uninstall.sh ]]; then
    log "running k3s-uninstall.sh (removes k3s + all cluster data)"
    /usr/local/bin/k3s-uninstall.sh || warn "k3s-uninstall.sh returned non-zero; continuing cleanup"
  else
    warn "k3s-uninstall.sh not found — k3s may not have been installed by us; skipping cluster teardown"
  fi
else
  log "--keep-cluster: leaving k3s in place; delete kuso namespaces manually if desired"
fi

# Host files the installer wrote. Removed regardless of --keep-cluster: the
# GitHub App credentials in particular must not silently survive.
if [[ -e /etc/kuso/github-app.env || -e /etc/kuso/github-app.pem ]]; then
  log "removing /etc/kuso/github-app.{env,pem}"
  rm -f /etc/kuso/github-app.env /etc/kuso/github-app.pem
  rmdir /etc/kuso 2>/dev/null || true
fi

if [[ -f /etc/rancher/k3s/registries.yaml ]]; then
  log "removing /etc/rancher/k3s/registries.yaml"
  rm -f /etc/rancher/k3s/registries.yaml
fi

if grep -q '\bkuso-registry\.kuso\.svc\.cluster\.local\b' /etc/hosts 2>/dev/null; then
  log "removing kuso-registry /etc/hosts entry"
  sed -i.kuso-uninstall.bak -E '/kuso-registry\.kuso\.svc\.cluster\.local/d' /etc/hosts
fi

log "kuso uninstalled. If you kept the cluster, remove kuso namespaces with:"
log "  kubectl get ns -o name | grep -E 'kuso' | xargs -r kubectl delete"
