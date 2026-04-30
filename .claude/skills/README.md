# `.claude/skills/`

Project-specific skills for AI agents working on kuso. Each `.md` file is a skill an agent can read at the start of a session to ramp on conventions, architecture, and workflow rules that aren't obvious from the code.

## Index

| Skill                                            | When to read it                                                                  |
| ------------------------------------------------ | -------------------------------------------------------------------------------- |
| [`repo-orientation.md`](./repo-orientation.md)   | Start of every session. What lives where, what not to touch.                     |
| [`rebrand-conventions.md`](./rebrand-conventions.md) | Before adding code or auditing leftover `kubero` strings.                    |
| [`crd-architecture.md`](./crd-architecture.md)   | Before touching the operator or thinking about CRDs.                             |
| [`agentic-workflow.md`](./agentic-workflow.md)   | Before making changes — sets ground rules for what to do without asking.         |
| [`mcp-development.md`](./mcp-development.md)     | Before touching `mcp/` — tool design rules, layout, how to add a new tool.       |

## Adding a skill

If you (an agent) learn something non-obvious about kuso that future agents will need, add it here:

1. New file: `<topic>.md`
2. YAML frontmatter:
   ```
   ---
   name: <topic>
   description: <one-line — when should this skill be loaded?>
   ---
   ```
3. Body: structured markdown. Tables and concrete examples > prose.
4. Add a row to the Index table above.

Keep skills tight. If a skill is over ~200 lines, split it.

## What goes here vs. `docs/`

- **`docs/`**: product-facing — PRD, architecture diagrams, user docs. Stable, versioned with releases.
- **`.claude/skills/`**: agent-facing — conventions, gotchas, "how we do things." Evolves continuously as the project matures.
