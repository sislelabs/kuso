// The bell's unread poll reads at most this many rows; a full page means
// the real number may be higher.
export const BELL_UNREAD_LIMIT = 200;

export interface BellCount {
  n: number;
  /** The poll hit BELL_UNREAD_LIMIT, so n is a lower bound. */
  atLeast: boolean;
}

export function bellCount(n: number, rawRows: number): BellCount {
  return { n, atLeast: rawRows >= BELL_UNREAD_LIMIT };
}

// Badge text: a 16px badge holds two glyphs, so it caps at "9+". The panel
// header carries the exact number.
export function bellBadgeText(c: BellCount): string {
  return c.n > 9 ? "9+" : String(c.n);
}

// The panel header and the trigger's aria-label: the same number the badge
// is derived from, with "+" when the poll was truncated.
export function bellUnreadLabel(c: BellCount): string {
  return `${c.n}${c.atLeast ? "+" : ""} unread`;
}
