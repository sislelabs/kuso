# syntax=docker/dockerfile:1.7
#
# kuso-incident-agent — the container the incident Job runs.
#
# It carries everything the autonomous incident-response agent needs to
# investigate a live incident and (on operator approval) implement a fix:
#
#   - Claude Code CLI (@anthropic-ai/claude-code) — the agent runtime.
#     We drive it head-less with `claude -p "<prompt>"`. It authenticates
#     with the operator's CC OAuth creds, mounted from the
#     kuso-incident-agent-cc Secret (see README.md + entrypoint.sh).
#   - kuso CLI — the documented investigation contract (logs/status/env/
#     builds). The investigate prompt drives THIS first, kubectl second.
#   - kubectl — read-only fallback (the investigate SA has read-only RBAC).
#   - git + gh + curl — clone the project repo, push the fix branch.
#   - jq + bash — entrypoint glue (parse CONTEXT_PACK, POST findings/pr).
#
# Built + pushed by `make incident-agent-image` (mirrors the other
# build/* images). Pin KUSO_CLI_VERSION to the release whose CLI artifact
# we download; defaults to the current server version at build time.
#
# Base: node:20-bookworm-slim because the Claude Code CLI is an npm
# package and wants a real Node runtime — alpine/musl is unsupported by
# some of its native deps. Debian-slim also gives us apt for kubectl/gh/git.

FROM node:20-bookworm-slim

# Which kuso CLI release artifact to bake in. The CLI is published on the
# GitHub release as kuso-linux-<arch> (see hack/release.sh). Override at
# build time: --build-arg KUSO_CLI_VERSION=v0.18.54.
ARG KUSO_CLI_VERSION=v0.18.54
ARG TARGETARCH=amd64
# Optional: COPY a locally-built CLI instead of downloading. Set to a path
# under the build context (e.g. dist/kuso-linux-amd64) and pass
# --build-arg KUSO_CLI_LOCAL=dist/kuso-linux-amd64. Empty → download.
ARG KUSO_CLI_LOCAL=

# Claude Code CLI version. Pin so a CC release doesn't silently change
# prompt/flag behaviour under us.
ARG CLAUDE_CODE_VERSION=latest

ENV DEBIAN_FRONTEND=noninteractive

# System tooling: kubectl + gh come from their official apt repos; the
# rest (git, curl, jq, ca-certificates, gnupg) from Debian main.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      gnupg \
      git \
      jq \
      bash \
      apt-transport-https; \
    # --- kubectl (pinned stable channel, official Kubernetes apt repo) ---
    mkdir -p /etc/apt/keyrings; \
    curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.30/deb/Release.key \
      | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg; \
    echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.30/deb/ /' \
      > /etc/apt/sources.list.d/kubernetes.list; \
    # --- gh (GitHub CLI, official apt repo) ---
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
      | gpg --dearmor -o /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg; \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
      > /etc/apt/sources.list.d/github-cli.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends kubectl gh; \
    rm -rf /var/lib/apt/lists/*

# Claude Code CLI. Installed globally via npm; `claude` lands on $PATH.
RUN --mount=type=cache,target=/root/.npm \
    npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && claude --version

COPY entrypoint.sh /usr/local/bin/incident-agent-entrypoint
COPY prompts/ /opt/incident-agent/prompts/

# kuso CLI. The whole build context is staged at /staging; when a
# locally-built binary is present there (KUSO_CLI_LOCAL=<path-in-context>,
# e.g. dist/kuso-linux-amd64) we install it, otherwise we download the
# matching artifact from the GitHub release. The bind-mount keeps the
# local binary out of an image layer (it's only read during this RUN).
RUN --mount=type=bind,target=/staging,source=. \
    set -eux; \
    if [ -n "${KUSO_CLI_LOCAL}" ] && [ -f "/staging/${KUSO_CLI_LOCAL}" ]; then \
      install -m 0755 "/staging/${KUSO_CLI_LOCAL}" /usr/local/bin/kuso; \
    else \
      curl -fsSL \
        "https://github.com/sislelabs/kuso/releases/download/${KUSO_CLI_VERSION}/kuso-linux-${TARGETARCH}" \
        -o /usr/local/bin/kuso; \
      chmod 0755 /usr/local/bin/kuso; \
    fi; \
    /usr/local/bin/kuso --version || /usr/local/bin/kuso version || true; \
    chmod 0755 /usr/local/bin/incident-agent-entrypoint

# The agent runs read-only against the cluster and clones into a scratch
# workdir. Run as a non-root, unprivileged user (node:20 ships `node`,
# uid 1000) with a writable HOME for ~/.claude and the git worktree. Create
# the workdir owned by node so the entrypoint can write the cloned repo +
# ~/.claude/.credentials.json without root.
ENV HOME=/home/node
RUN mkdir -p /home/node/work /home/node/.claude \
 && chown -R node:node /home/node
USER node
WORKDIR /home/node/work

# kubectl uses the pod's mounted SA token automatically (in-cluster
# config); no kubeconfig baked in. The Job mounts the read-only SA.
ENTRYPOINT ["/usr/local/bin/incident-agent-entrypoint"]
