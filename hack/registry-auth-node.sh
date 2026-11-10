#!/bin/bash
# Put the kuso-registry credential on ONE existing k3s node (step 3 of the
# staged registry-auth rollout, see deploy/registry.yaml). Run as root on
# the node; feed the password on stdin so it stays out of shell history
# and `ps`:
#
#   scp hack/registry-auth-node.sh root@<node>:/tmp/
#   kubectl -n kuso get secret kuso-registry-auth -o jsonpath='{.data.password}' \
#     | base64 -d | ssh root@<node> bash /tmp/registry-auth-node.sh
#
# Rewrites /etc/rancher/k3s/registries.yaml with the same mirror/TLS block
# install.sh and the join bootstrap write, plus the credential and the
# marker line the server's probe looks for, then restarts k3s (control
# plane) or k3s-agent so containerd reloads it. Pods keep running across a
# k3s restart; do one node at a time anyway and check for crashloops after
# (memory: an agent restart once orphaned buildkitd).
set -euo pipefail

HOST="${KUSO_REGISTRY_HOST:-kuso-registry.kuso.svc.cluster.local:5000}"
USER_NAME="${KUSO_REGISTRY_USER:-kuso}"
FILE=/etc/rancher/k3s/registries.yaml

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
IFS= read -r PASS || true
[ -n "${PASS:-}" ] || { echo "no password on stdin" >&2; exit 1; }
case "$PASS" in *[!0-9a-f]*) echo "password must be the hex value from kuso-registry-auth" >&2; exit 1 ;; esac

MARKER=$(printf '%s' "$USER_NAME:$PASS" | sha256sum | cut -c1-16)
mkdir -p /etc/rancher/k3s
[ -f "$FILE" ] && cp -p "$FILE" "$FILE.bak.$(date +%s)"
umask 077
cat > "$FILE.new" <<EOF
mirrors:
  "$HOST":
    endpoint:
      - "http://$HOST"
configs:
  "$HOST":
    tls:
      insecure_skip_verify: true
    auth:
      username: "$USER_NAME"
      password: "$PASS"
# kuso-registry-auth: $MARKER
EOF
mv "$FILE.new" "$FILE"
chmod 600 "$FILE"

if systemctl is-active --quiet k3s; then
  systemctl restart k3s
elif systemctl is-active --quiet k3s-agent; then
  systemctl restart k3s-agent
else
  echo "neither k3s nor k3s-agent is active; restart it yourself" >&2
  exit 1
fi
echo "registries.yaml updated (marker $MARKER); k3s restarted"
