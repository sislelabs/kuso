#!/usr/bin/env bash
# Delete the sandbox: containers, volumes and local state.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

compose --profile tools down --volumes --remove-orphans
rm -rf "$STATE_DIR"
log "sandbox removed"
