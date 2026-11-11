#!/usr/bin/env bash
# Health of the Docker host the sandboxes share: disk (prunes build cache
# when low, same rule as `up`), and every sandbox instance with its ports,
# memory and volume sizes. Never removes images, volumes or containers.
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

docker info >/dev/null 2>&1 || die "the docker daemon is not reachable"
disk_guard
echo
docker system df
echo

log "sandbox instances (current in this checkout: ${KUSO_SANDBOX_NAME})"
nodes="$(docker ps -a --filter label=com.docker.compose.service=node --format '{{.Label "com.docker.compose.project"}}' | sort -u)"
if [[ -z "$nodes" ]]; then
  echo "  none"
  exit 0
fi
# "VOLUME NAME  LINKS  SIZE" rows of the verbose table.
volumes_df="$(docker system df -v 2>/dev/null | awk 'NF == 3 { print $1, $3 }' || true)"
printf '  %-18s %-8s %-22s %-12s %s\n' NAME STATE PORTS MEMORY VOLUMES
for n in $nodes; do
  state="$(docker inspect -f '{{.State.Status}}' "${n}-node" 2>/dev/null || echo missing)"
  ports="$(docker inspect -f '{{range $k, $v := .HostConfig.PortBindings}}{{range $v}}{{.HostPort}} {{end}}{{end}}' "${n}-node" 2>/dev/null | tr ' ' '\n' | sort -un | xargs)"
  mem="-"
  [[ "$state" == "running" ]] && mem="$(docker stats --no-stream --format '{{.MemUsage}}' "${n}-node" | cut -d/ -f1 | xargs)"
  vols="$(awk -v p="${n}_" 'index($1, p) == 1 { printf "%s=%s ", substr($1, length(p) + 1), $2 }' <<<"$volumes_df")"
  printf '  %-18s %-8s %-22s %-12s %s\n' "$n" "$state" "${ports:--}" "$mem" "${vols:--}"
done
