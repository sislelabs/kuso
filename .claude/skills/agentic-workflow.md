---
name: agentic-workflow
description: Use when you (the agent) are about to make changes to kuso. Sets ground rules for the agentic-first development model — what to do without asking, what to confirm, and how to keep work atomic.
---

# How agents should work in kuso

kuso is built **for** AI agents and **with** AI agents. Most operational and development work flows through Claude Code sessions — humans drive intent, agents drive implementation. That changes the rules in a few ways.

## What you can do without asking

- Read any file in the repo.
- Run any non-destructive command: `git status`, `git log`, `git diff`, `go build ./...`, `pnpm lint` (in `web/`), `kubectl get`, `kubectl describe`, `kuso status`, `kuso logs`, `kuso get …`, `kuso service errors/pods`.
- Edit code in `server-go/`, `web/`, `operator/`, `cli/`, `mcp/`.
- Create new branches, write commits on a branch, run tests.
- Write to `.claude/skills/` to capture new conventions you've learned.
- Update `docs/` as you go.

## What requires explicit confirmation

- `git push`, `gh pr create`, anything that hits the network with state changes.
- `kubectl apply`, `kubectl delete`, anything that mutates a real cluster.
- `kuso build trigger` / `kuso redeploy` / `kuso project service stop|start` / `kuso apply`, anything that affects running production apps.
- `git reset --hard`, `git checkout --`, `rm -rf` on tracked files — anything destructive to local state.
- Adding new dependencies (pnpm/go modules).
- Editing `LICENSE`, `NOTICE`, or the attribution paragraph in root `README.md`.

## Atomic commits

The history is intentionally structured. Each commit should be one logical change with a clear message. Examples of good commits:

- `feat(cli): add kuso logs --follow`
- `feat(operator): support envFrom on KusoService`
- `refactor: extract sleep policy into its own helm chart`
- `fix(server): handle race in deploy webhook`

Bad commits to avoid:

- "WIP", "fixes", "stuff" — write descriptive messages.
- Mega-commits touching server + operator + cli for unrelated changes.
- Commits that leave the build broken (each commit should pass `go build` and `pnpm build` for the affected subdir).

## When you find something surprising

If you discover unfamiliar files, branches, or configs, **investigate before deleting or overwriting** — it's likely Ivo's in-progress work. Read first, ask second, write third.

## Skills are living docs

If you learn something non-obvious about the codebase that future agents will also need (a hidden invariant, a non-standard build step, a gotcha), **add or update a skill in `.claude/skills/`**. That's how kuso accumulates institutional knowledge across sessions.

When in doubt about whether something belongs in a skill: if you found yourself reading the same file twice to figure something out, it's a skill.

## What the maintainer cares about

In rough order:

1. **Agent ergonomics.** kuso should feel like a first-class tool to drive from Claude Code. Structured outputs, idempotent ops, intent-grouped MCP tools.
2. **Sleeping containers.** This is the killer feature. Don't break it.
3. **YAGNI discipline.** No abstractions for hypothetical future requirements. No half-finished refactors. Three similar lines beats a premature abstraction.
4. **No legacy compatibility.** kuso is its own thing. Design for the current shape; don't bend to old upstream conventions.
