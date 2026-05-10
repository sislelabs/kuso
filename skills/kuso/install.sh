#!/usr/bin/env bash
# Install the kuso Claude-Code skill into the current project.
#
# Usage (from inside your project repo):
#
#   curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/install.sh | bash
#
# What it does:
#   1. mkdir -p .claude/skills/kuso
#   2. download SKILL.md into it
#   3. print next-steps so the user can verify
#
# Idempotent: re-running overwrites SKILL.md with the latest from main.
# That's intentional — when kuso ships a new release with new CLI
# surface, re-curling picks it up.

set -euo pipefail

REF="${KUSO_SKILL_REF:-main}"
RAW_BASE="https://raw.githubusercontent.com/sislelabs/kuso/${REF}/skills/kuso"

# We install to .claude/skills/<name>/SKILL.md — the path Claude Code
# looks for project-scoped skills. If the user runs this from a
# subdirectory that isn't a repo root, we still drop the skill in
# CWD/.claude/skills/kuso (warning printed). They can move the dir
# afterwards if needed.
TARGET_DIR="$(pwd)/.claude/skills/kuso"

if [[ ! -d "$(pwd)/.git" ]]; then
  echo "warning: $(pwd) is not a git repo root — installing to ${TARGET_DIR} anyway" >&2
fi

echo "==> installing kuso skill into ${TARGET_DIR}"
mkdir -p "${TARGET_DIR}"

# Pull the SKILL.md. -f = fail on HTTP error so we don't silently
# write a 404 page into the skill file. -L follows redirects in case
# the repo ever moves.
if ! curl -fsSL "${RAW_BASE}/SKILL.md" -o "${TARGET_DIR}/SKILL.md.tmp"; then
  echo "error: failed to download ${RAW_BASE}/SKILL.md" >&2
  rm -f "${TARGET_DIR}/SKILL.md.tmp"
  exit 1
fi
mv "${TARGET_DIR}/SKILL.md.tmp" "${TARGET_DIR}/SKILL.md"

# Track the version we installed so future runs of this script can
# detect drift / show a "newer version available" hint.
{
  echo "ref: ${REF}"
  echo "installedAt: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "source: ${RAW_BASE}/SKILL.md"
} > "${TARGET_DIR}/.installed"

cat <<'NEXT'
==> done.

The skill is now active for Claude Code sessions in this project.
Verify with:

  /skills

You should see "kuso" listed. If you don't, restart your Claude
Code session — skills are loaded at session start.

To update later, re-run:

  curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/install.sh | bash

To uninstall:

  rm -rf .claude/skills/kuso
NEXT
