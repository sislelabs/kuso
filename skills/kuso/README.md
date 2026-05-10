# Claude Code skill: kuso

A drop-in [Claude Code skill](https://docs.claude.com/en/docs/claude-code/skills) that teaches Claude how to operate a project deployed to [kuso](https://github.com/sislelabs/kuso) — the CLI surface, deployment lifecycle, env-var reference syntax, and the standard debugging playbook.

When this skill is installed, Claude reaches for `kuso` (not raw `kubectl`) for cluster operations, knows what `${{ db.DATABASE_URL }}` resolves to, and walks the right diagnostic order when a build fails or a pod won't come up.

## Install (one-liner)

From your project repo root:

```bash
curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/install.sh | bash
```

That writes `.claude/skills/kuso/SKILL.md` and tracks the install in `.claude/skills/kuso/.installed`. Restart your Claude Code session and `/skills` will show **kuso** in the active list.

## Manual install

If you'd rather not pipe a script:

```bash
mkdir -p .claude/skills/kuso
curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/SKILL.md \
  -o .claude/skills/kuso/SKILL.md
```

## Update

Re-run the same `curl` — `install.sh` overwrites `SKILL.md` with the latest from `main`. Pin to a specific kuso release with:

```bash
KUSO_SKILL_REF=v0.9.58 curl -fsSL \
  "https://raw.githubusercontent.com/sislelabs/kuso/${KUSO_SKILL_REF}/skills/kuso/install.sh" | bash
```

## Uninstall

```bash
rm -rf .claude/skills/kuso
```

## What's in the skill

- **Mental model** — projects, services, environments, addons, builds
- **The 12 CLI commands** you'll actually use, with concrete shapes
- **How a deploy actually flows** — push → build CR → kaniko → image promote → kube roll
- **Failure modes** ranked by frequency: GH App not installed, OOMKilled snapshot, wrong port, NEXTAUTH_URL mismatch, CrashLoopBackOff
- **`${{ ... }}` env-var reference syntax** — addon refs, service-to-service refs, the rules
- **Debugging playbook** — the standard 7-step order
- **Edit safety** — what's hot-swappable vs. what triggers a rollout
- **`kuso.yml` shape** for config-as-code

The skill content is in [`SKILL.md`](./SKILL.md). It's a single file by design — every Claude Code session pays the token cost of loading active skills, so we keep it tight.

## Requirements

- The `kuso` CLI on PATH (`brew install` / `kuso` releases on GitHub).
- A logged-in session: `kuso login --api https://kuso.<your-domain> --token <pat>`.
- Verify both with: `kuso doctor`.

## License

MIT — same as kuso itself.
