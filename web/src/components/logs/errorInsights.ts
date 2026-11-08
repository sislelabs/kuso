import { stripAnsi } from "./logParse";

export interface BuildLike {
  status: string;
  finishedAt?: string;
  commitSha?: string;
  ref?: string;
  id: string;
}

// buildLiveAt returns the build that was serving when an error first
// appeared: the newest successful build finished at or before `at`.
// Returns null when `at` predates every known build, or when firstSeen
// is pinned to the start of the query window (it's then only "first in
// this window", not when the error began).
export function buildLiveAt<B extends BuildLike>(
  builds: readonly B[],
  at: string,
  windowStart: number,
): { build: B; afterMs: number } | null {
  const t = Date.parse(at);
  if (Number.isNaN(t) || t - windowStart < 10 * 60_000) return null;
  let best: B | null = null;
  let bestT = -Infinity;
  for (const b of builds) {
    if (b.status !== "succeeded" || !b.finishedAt) continue;
    const ft = Date.parse(b.finishedAt);
    if (Number.isNaN(ft) || ft > t || ft <= bestT) continue;
    best = b;
    bestT = ft;
  }
  return best ? { build: best, afterMs: t - bestT } : null;
}

const FRAME = /^(at |File "|\S+\.(go|py|js|ts|mjs|cjs|rb|java|kt|rs|php|cs):\d+|\S+\(.*:\d+(:\d+)?\)|goroutine \d+|Caused by)/;

// traceFrames picks up to `max` stack-frame lines that follow the
// sample line in the archive (same pod, same instant). Frame-shaped
// lines win; otherwise the first non-empty follow-on lines.
export function traceFrames(lines: readonly string[], sample: string, max = 3): string[] {
  const raw = stripAnsi(sample).trim();
  const start = lines.findIndex((l) => stripAnsi(l).trim() === raw);
  if (start < 0) return [];
  const rest = lines
    .slice(start + 1, start + 60)
    .map((l) => stripAnsi(l).trim())
    .filter(Boolean);
  const frames = rest.filter((l) => FRAME.test(l));
  return (frames.length > 0 ? frames : rest).slice(0, max);
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}
