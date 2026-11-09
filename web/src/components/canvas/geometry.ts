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

// serviceNodeWidth sizes a service card to its name: 280px base, growing
// for longer names up to 380px, past which the name truncates (with a
// tooltip). The estimate is ~7.6px per character of 14px medium text
// plus the icon, padding and the actions button.
export const SERVICE_NODE_MIN_WIDTH = 280;
export const SERVICE_NODE_MAX_WIDTH = 380;
export function serviceNodeWidth(name: string): number {
  const est = Math.round(76 + name.length * 7.6);
  return Math.min(SERVICE_NODE_MAX_WIDTH, Math.max(SERVICE_NODE_MIN_WIDTH, est));
}
