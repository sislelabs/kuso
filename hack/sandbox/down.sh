#!/usr/bin/env bash
# Delete the current sandbox instance: containers, volumes and its state.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

compose --profile tools down --volumes --remove-orphans
rm -rf "$STATE_DIR"
if [[ "$(cat "${STATE_ROOT}/current" 2>/dev/null)" == "$KUSO_SANDBOX_NAME" ]]; then
  rm -f "${STATE_ROOT}/current"
fi
log "sandbox ${KUSO_SANDBOX_NAME} removed"
