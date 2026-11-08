import type { PodMetric } from "./api";

// metrics-server only knows "now". Newer servers keep a 24h history
// (getServiceUsageHistory); against older ones the Metrics tab builds a
// short history from its own polls, kept per env for the page session
// so leaving and re-opening the tab doesn't start from zero.

export interface ResourceSample {
  t: number; // epoch ms
  cpuMillicores: number;
  memBytes: number;
  pods: number;
}

const MAX_AGE_MS = 6 * 3_600_000;
const store = new Map<string, ResourceSample[]>();

export function getSamples(env: string): ResourceSample[] {
  return store.get(env) ?? [];
}

// recordSample appends a poll result; returns the new array (a fresh
// reference only when something was added).
export function recordSample(env: string, pods: PodMetric[], now: number): ResourceSample[] {
  const prev = store.get(env) ?? [];
  const stamp = pods.map((p) => Date.parse(p.timestamp ?? "")).filter((n) => !Number.isNaN(n));
  const t = stamp.length ? Math.max(...stamp) : now;
  // metrics-server refreshes every ~15s; a repeat timestamp is the same sample.
  if (prev.length && prev[prev.length - 1].t === t) return prev;
  const sample: ResourceSample = {
    t,
    cpuMillicores: pods.reduce((a, p) => a + p.cpuMillicores, 0),
    memBytes: pods.reduce((a, p) => a + p.memBytes, 0),
    pods: pods.length,
  };
  const next = [...prev.filter((s) => s.t >= now - MAX_AGE_MS && s.t < t), sample];
  store.set(env, next);
  return next;
}

// mergeSamples puts the live polls after the server history, dropping
// any that don't come after the last server point.
export function mergeSamples(server: ResourceSample[], live: ResourceSample[]): ResourceSample[] {
  if (server.length === 0) return live;
  const lastT = server[server.length - 1].t;
  const after = live.filter((s) => s.t > lastT);
  return after.length ? [...server, ...after] : server;
}

export function resetSamplesForTest(): void {
  store.clear();
}
