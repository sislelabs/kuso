// Pure geometry for the canvas: which node an arrow key moves to, the
// reading order Tab/j/k follow, and which sides an edge leaves/enters.

export interface Box {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export type Direction = "up" | "down" | "left" | "right";
export type Side = "top" | "right" | "bottom" | "left";

const center = (b: Box) => ({ cx: b.x + b.w / 2, cy: b.y + b.h / 2 });

// readingOrder sorts boxes top-to-bottom, then left-to-right. Boxes whose
// centres sit within half a row of each other count as the same row, so
// a few pixels of drag jitter doesn't reorder a visually aligned row.
export function readingOrder(boxes: readonly Box[]): Box[] {
  const rowTolerance = 40;
  return [...boxes].sort((a, b) => {
    const ca = center(a);
    const cb = center(b);
    if (Math.abs(ca.cy - cb.cy) > rowTolerance) return ca.cy - cb.cy;
    return ca.cx - cb.cx || (a.id < b.id ? -1 : 1);
  });
}

// nextInDirection picks the nearest box whose centre lies in the pressed
// direction, preferring boxes straight ahead over ones off to the side
// (sideways distance costs triple). Returns null at the edge of the graph.
export function nextInDirection(boxes: readonly Box[], fromId: string, dir: Direction): string | null {
  const from = boxes.find((b) => b.id === fromId);
  if (!from) return null;
  const { cx, cy } = center(from);
  let best: { id: string; score: number } | null = null;
  for (const b of boxes) {
    if (b.id === fromId) continue;
    const c = center(b);
    const dx = c.cx - cx;
    const dy = c.cy - cy;
    const ahead = dir === "right" ? dx : dir === "left" ? -dx : dir === "down" ? dy : -dy;
    const side = dir === "left" || dir === "right" ? Math.abs(dy) : Math.abs(dx);
    // Must be ahead, and more ahead than sideways-ish (a 63° cone).
    if (ahead <= 1 || side > ahead * 2) continue;
    const score = ahead + side * 3;
    if (!best || score < best.score) best = { id: b.id, score };
  }
  return best?.id ?? null;
}

// facingSides returns the side of each box that faces the other, so an
// edge leaves and enters on the short way round. Horizontal sides win
// unless the boxes are clearly stacked, which keeps the left-to-right
// layout's edges horizontal.
export function facingSides(source: Box, target: Box): { source: Side; target: Side } {
  const s = center(source);
  const t = center(target);
  const dx = t.cx - s.cx;
  const dy = t.cy - s.cy;
  // Stacked: boxes overlap horizontally, so a side-to-side edge would
  // have to loop around.
  const overlapX = Math.min(source.x + source.w, target.x + target.w) - Math.max(source.x, target.x);
  if (overlapX > 0 && Math.abs(dy) > Math.abs(dx) * 0.5) {
    return dy >= 0 ? { source: "bottom", target: "top" } : { source: "top", target: "bottom" };
  }
  return dx >= 0 ? { source: "right", target: "left" } : { source: "left", target: "right" };
}

// Each side of a node carries PORT_SLOTS anchor points, evenly spaced, so
// edges meeting one side spread out instead of stacking on its midpoint.
export const PORT_SLOTS = 7;

// slotFraction is where slot i sits along its side (0 = top/left end).
export function slotFraction(i: number): number {
  return (i + 1) / (PORT_SLOTS + 1);
}

// slotsFor spreads n edges over the side's slots, centred: one edge takes
// the middle slot, two sit either side of it, and so on. Past
// PORT_SLOTS edges the slots are shared.
export function slotsFor(n: number): number[] {
  return Array.from({ length: n }, (_, i) =>
    Math.min(PORT_SLOTS - 1, Math.max(0, Math.round(((i + 1) * (PORT_SLOTS + 1)) / (n + 1)) - 1)),
  );
}

export interface EdgePorts {
  sourceHandle: string;
  targetHandle: string;
  // Where the bend sits between the two ends (smoothstep stepPosition):
  // edges into different ports bend at different points, so parallel
  // edges between two columns don't share one vertical lane.
  stepPosition: number;
}

// assignPorts picks, for every edge, the facing sides of its two nodes
// and a distinct slot on each. Edges sharing a node side are ordered by
// where their other end sits along that side, so they fan out without
// crossing each other at the node.
export function assignPorts(
  edges: readonly { id: string; source: string; target: string }[],
  boxById: ReadonlyMap<string, Box>,
): Map<string, EdgePorts> {
  type End = { edge: string; end: "source" | "target"; key: string; side: Side; other: number };
  const ends: End[] = [];
  for (const e of edges) {
    const s = boxById.get(e.source);
    const t = boxById.get(e.target);
    if (!s || !t) continue;
    const sides = facingSides(s, t);
    const along = (b: Box, side: Side) => {
      const c = center(b);
      return side === "left" || side === "right" ? c.cy : c.cx;
    };
    ends.push({ edge: e.id, end: "source", key: `${e.source}|${sides.source}`, side: sides.source, other: along(t, sides.source) });
    ends.push({ edge: e.id, end: "target", key: `${e.target}|${sides.target}`, side: sides.target, other: along(s, sides.target) });
  }
  const groups = new Map<string, End[]>();
  for (const end of ends) {
    const g = groups.get(end.key);
    if (g) g.push(end);
    else groups.set(end.key, [end]);
  }
  const slot = new Map<string, { side: Side; slot: number }>();
  for (const g of groups.values()) {
    g.sort((a, b) => a.other - b.other || (a.edge < b.edge ? -1 : 1));
    const slots = slotsFor(g.length);
    g.forEach((end, i) => slot.set(`${end.edge}|${end.end}`, { side: end.side, slot: slots[i] }));
  }
  const out = new Map<string, EdgePorts>();
  const mid = (PORT_SLOTS - 1) / 2;
  for (const e of edges) {
    const s = slot.get(`${e.id}|source`);
    const t = slot.get(`${e.id}|target`);
    if (!s || !t) continue;
    out.set(e.id, {
      sourceHandle: portId(s.side, s.slot),
      targetHandle: portId(t.side, t.slot),
      stepPosition: 0.5 + ((t.slot - mid) / mid) * 0.25,
    });
  }
  return out;
}

export function portId(side: Side, slot: number): string {
  return `${side}-${slot}`;
}

// serviceNodeWidth sizes a service card to its name: 280px base, growing
// for longer names up to 380px, past which the name truncates (with a
// tooltip). The estimate is ~7.6px per character of 14px medium text
// plus the icon, padding and the actions button.
export const SERVICE_NODE_MIN_WIDTH = 280;
// Compact density: one size for every service card (3 grid units tall,
// like an addon).
export const COMPACT_SERVICE_NODE_WIDTH = 240;
export const COMPACT_SERVICE_NODE_HEIGHT = 72;
export const SERVICE_NODE_MAX_WIDTH = 380;
export function serviceNodeWidth(name: string): number {
  const est = Math.round(76 + name.length * 7.6);
  return Math.min(SERVICE_NODE_MAX_WIDTH, Math.max(SERVICE_NODE_MIN_WIDTH, est));
}

// Addon and cron cards start at 220px and widen the same way, so a name
// up to ~20 characters shows in full. `chrome` is the card's fixed
// horizontal cost (padding, icon, actions button).
export const SMALL_NODE_MIN_WIDTH = 220;
export const SMALL_NODE_MAX_WIDTH = 340;
export const ADDON_NODE_CHROME = 82;
export const CRON_NODE_CHROME = 72;
export function smallNodeWidth(name: string, chrome: number): number {
  const est = Math.round(chrome + name.length * 7.6);
  return Math.min(SMALL_NODE_MAX_WIDTH, Math.max(SMALL_NODE_MIN_WIDTH, est));
}

// addonNodeWidth also leaves room for the status row ("Live · ClickHouse
// · 10Gi") and, when shown, the CVE chip that rides at its end.
export function addonNodeWidth(name: string, statusRow: string, cveChip: boolean): number {
  const row = Math.round(ADDON_NODE_CHROME - 8 + statusRow.length * 7 + (cveChip ? 84 : 0));
  return Math.min(SMALL_NODE_MAX_WIDTH, Math.max(smallNodeWidth(name, ADDON_NODE_CHROME), row));
}
