#!/usr/bin/env bash
#
# git wrapper for the incident agent. The implement phase lets the agent
# edit files and run `git status/diff/log/show/add/commit`. Git runs
# programs named in config (core.fsmonitor, diff.external, core.pager,
# filter/diff/merge drivers, hooks, credential helpers, sshCommand ...),
# and the checkout's .git/config is writable by the agent. So:
#
#   - system and global config are not read; the settings git needs come
#     from GIT_CONFIG_COUNT, which outranks every config file and pins the
#     fixed-name executors to no-ops (diff.external has no "off" value, an
#     empty one makes every diff fail, so it is left to the check below);
#   - the repo's own config may only hold the plain keys `git clone` and
#     `git checkout -b` write. Anything else (an fsmonitor, a driver, an
#     include, worktree config) makes every git call refuse to run;
#   - no transport is allowed, so nothing can be fetched or pushed;
#   - global options (-c, -C, --git-dir, --exec-path ...) are refused.
#
# The real binary is /usr/bin/git; the entrypoint calls it directly for
# its own trusted clone and push.
set -euo pipefail

real="${KUSO_GIT_REAL:-/usr/bin/git}"

case "${1:-}" in
  -*)
    echo "git: global option ${1%%=*} is not allowed in the incident agent" >&2
    exit 2
    ;;
esac

unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_CEILING_DIRECTORIES \
  GIT_EXEC_PATH GIT_TEMPLATE_DIR GIT_CONFIG GIT_CONFIG_PARAMETERS \
  GIT_EXTERNAL_DIFF GIT_DIFF_OPTS GIT_SSH GIT_SSH_COMMAND GIT_PROXY_COMMAND \
  GIT_ASKPASS SSH_ASKPASS GIT_PAGER PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR \
  VISUAL EDITOR GIT_ATTR_SOURCE GIT_REPLACE_REF_BASE GIT_TRACE GIT_TRACE2 \
  GIT_TRACE2_EVENT GIT_TRACE2_PERF

export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0
export GIT_PAGER=cat GIT_EDITOR=true GIT_SEQUENCE_EDITOR=true

overrides=(
  "core.hooksPath=/dev/null"
  "core.fsmonitor=false"
  "core.pager=cat"
  "core.editor=true"
  "core.sshCommand=false"
  "credential.helper="
  "protocol.allow=never"
  "commit.gpgSign=false"
  "tag.gpgSign=false"
  "sequence.editor=true"
  "user.name=kuso-incident-agent"
  "user.email=incident-agent@kuso.local"
)
export GIT_CONFIG_COUNT="${#overrides[@]}"
for i in "${!overrides[@]}"; do
  export "GIT_CONFIG_KEY_${i}=${overrides[$i]%%=*}"
  export "GIT_CONFIG_VALUE_${i}=${overrides[$i]#*=}"
done

# Inside a repository, refuse to run if its config names anything beyond
# what clone/checkout write. --no-includes lists include.path itself
# instead of following it, so includes are refused too.
if "$real" rev-parse --git-dir >/dev/null 2>&1; then
  if ! keys="$("$real" config --local --no-includes --name-only --list -z 2>/dev/null | tr '\0' '\n')"; then
    echo "git: could not read this repository's config; refusing" >&2
    exit 2
  fi
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    case "$key" in
      core.repositoryformatversion|core.filemode|core.bare|core.logallrefupdates|\
      core.ignorecase|core.precomposeunicode|core.symlinks|\
      remote.origin.url|remote.origin.fetch|\
      branch.*.remote|branch.*.merge) ;;
      *)
        echo "git: this repository's config sets '${key}', which the incident agent does not allow; refusing" >&2
        exit 2
        ;;
    esac
  done <<<"$keys"
fi

exec "$real" "$@"
