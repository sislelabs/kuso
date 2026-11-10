# `.claude/skills/`

Project skills for agents working on the kuso repo. Claude Code loads each `<name>/SKILL.md` and decides when to use it from the frontmatter `description`. Repo layout, architecture and cluster rules live in the root `CLAUDE.md`, which is loaded every turn.

| Skill | Load it when |
| --- | --- |
| [`agentic-workflow`](./agentic-workflow/SKILL.md) | Making changes in the repo or touching a live instance: what needs asking, commit style. |
| [`mcp-development`](./mcp-development/SKILL.md) | Touching `mcp/`: tool design rules, layout, adding a tool. |
| [`smoke-tests`](./smoke-tests/SKILL.md) | Before a PR: which test layer catches what, in cost order. |

The kuso skill for consumer repos (`skills/kuso/`) is a separate, published artifact.

## Adding a skill

1. Create `<name>/SKILL.md` with frontmatter `name: <name>` and a `description` that says when to load it ("Use when …").
2. Keep it short; tables and examples beat prose. Split anything over ~200 lines.
3. Add a row above.

Product and operator docs go in `docs/`. Agent conventions and gotchas go here.
