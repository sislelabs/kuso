import type { NodeStatusTone } from "@/features/services";

// Shared card recipe for every canvas node: a lit surface with real
// depth, a neutral hairline border, and status carried by the LED rail
// along the top edge (plus the dot + label inside). Only states that
// need a person tint the border too, so a healthy graph reads calm and a
// failing card still jumps out at a glance.
export const nodeCardClass =
  "group relative cursor-pointer rounded-2xl border bg-[var(--bg-elevated)] shadow-node transition-[box-shadow,border-color,opacity] duration-150 hover:shadow-node-hover";

const LOUD: ReadonlySet<NodeStatusTone> = new Set<NodeStatusTone>(["error", "warning", "progress"]);

export function nodeBorderClass(tone: NodeStatusTone, toneBorder: string): string {
  return LOUD.has(tone) ? toneBorder : "border-[var(--border-default)] hover:border-[var(--border-strong)]";
}

// The rail sits inside the rounded corners so it reads as a lit strip on
// the card's edge rather than a coloured top border.
export const nodeRailClass = "pointer-events-none absolute inset-x-4 top-0 h-[2px] rounded-b-full";

export const nodeActionButtonClass =
  "nodrag absolute right-2 top-2 z-10 inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--text-tertiary)] opacity-0 transition-opacity hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] focus:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100";

export const nodeNameClass = "truncate font-display text-sm font-semibold [font-stretch:100%] text-[var(--text-primary)]";
