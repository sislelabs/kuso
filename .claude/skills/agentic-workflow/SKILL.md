---
name: agentic-workflow
description: Use before making changes in the kuso repo or touching a live kuso instance. Sets what an agent may do without asking, what needs confirmation, and how to structure commits.
---

# How agents should work in kuso

kuso is built **for** AI agents and **with** AI agents. Most operational and development work flows through Claude Code sessions — humans drive intent, agents drive implementation. That changes the rules in a few ways.

## What you can do without asking

- Read any file in the repo.
- Run non-destructive local commands: `git status/log/diff`, `go build ./...`, `go test ./...`, `pnpm lint` / `pnpm typecheck` (in `web/`).
- Read-only `kuso` CLI calls against the live instance: `kuso status`, `kuso logs`, `kuso get …`, `kuso service errors/pods/events/crash`, `kuso build list`, `kuso env list`. Inspect the cluster through the CLI; use raw `kubectl get/describe` (over ssh, see CLAUDE.md) only in the cases CLAUDE.md lists, such as non-kuso pods, operator CRD state or cluster-level objects.
- Edit code in `server-go/`, `web/`, `operator/`, `cli/`, `mcp/`.
- Create branches and run tests. Commit only when the user asks.
- Write to `.claude/skills/` to capture new conventions you've learned.
- Update `docs/` as you go.

## What requires explicit confirmation

The live instance runs real personal and client apps. Treat it as read-only unless the user says otherwise, and that includes disposable test projects on it.

- Any mutation through the CLI or API: `kuso build trigger`, `kuso redeploy`, `kuso env set`, `kuso secret set`, `kuso service restart`, `kuso apply`, `kuso project service stop|start`, `kuso upgrade`, `kuso api POST/PUT/PATCH/DELETE …`.
- Any `kubectl apply/delete/patch/edit` or other cluster mutation.
- `git push`, `gh pr create`, or any other network call that changes state.
- `git reset --hard`, `git checkout --`, `rm -rf` on tracked files, or anything else that destroys local state.
- Adding new dependencies (pnpm/go modules).
- Editing `LICENSE`, `NOTICE`, or the attribution paragraph in root `README.md`.

## Atomic commits

The history is intentionally structured. Each commit should be one logical change with a clear message: an imperative, plain-English subject that says what changed for the user, no conventional-commit prefix, no trailing period. Examples from the log:

- `Keep the shared instance server reachable from every project`
- `Seed preview jobs from the image their build actually recorded`
- `Default the helm history pruner to dryrun and tighten what it may delete`
- `Pace helm history pruning`

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
