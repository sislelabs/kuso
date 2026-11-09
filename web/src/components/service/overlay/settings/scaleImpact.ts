// Aggregate CPU/memory a scale setting reserves, from per-pod Kubernetes
// quantities. Pure so the Scale card's summary is testable.

const MEM_UNITS: Record<string, number> = {
  "": 1,
  k: 1e3,
  K: 1e3,
  M: 1e6,
  G: 1e9,
  T: 1e12,
  Ki: 1024,
  Mi: 1024 ** 2,
  Gi: 1024 ** 3,
  Ti: 1024 ** 4,
};

// parseCPU returns cores ("100m" → 0.1, "2" → 2), or null when blank or
// not a quantity.
export function parseCPU(q: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)(m?)\s*$/.exec(q);
  if (!m) return null;
  const n = Number(m[1]);
  return m[2] === "m" ? n / 1000 : n;
}

// parseMemory returns bytes ("128Mi", "1Gi", "500M"), or null.
export function parseMemory(q: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)(Ki|Mi|Gi|Ti|k|K|M|G|T)?\s*$/.exec(q);
  if (!m) return null;
  return Number(m[1]) * MEM_UNITS[m[2] ?? ""];
}

export function formatCPU(cores: number): string {
  if (cores < 1) return `${Math.round(cores * 1000)}m`;
  return `${Number(cores.toFixed(2))}`;
}

export function formatMemory(bytes: number): string {
  const gi = bytes / 1024 ** 3;
  if (gi >= 1) return `${Number(gi.toFixed(2))}Gi`;
  return `${Math.round(bytes / 1024 ** 2)}Mi`;
}

export interface ScaleImpact {
  // Replica bounds one environment runs (min is at least 1 for the
  // reservation: a sleeping service still reserves while awake).
  minPods: number;
  maxPods: number;
  environments: number;
  // Per-environment totals at min and max pods; null = no request/limit.
  cpuRequest: [number, number] | null;
  memRequest: [number, number] | null;
  cpuLimit: [number, number] | null;
  memLimit: [number, number] | null;
}

export function scaleImpact(input: {
  min: string;
  max: string;
  cpuRequest: string;
  memRequest: string;
  cpuLimit: string;
  memLimit: string;
  environments: number;
}): ScaleImpact | null {
  const min = Number(input.min);
  const max = Number(input.max);
  if (!Number.isInteger(min) || !Number.isInteger(max) || min < 0 || max < 1) return null;
  const lo = Math.max(min, 1);
  const hi = Math.max(max, lo);
  const span = (v: number | null): [number, number] | null => (v === null ? null : [v * lo, v * hi]);
  return {
    minPods: lo,
    maxPods: hi,
    environments: Math.max(input.environments, 1),
    cpuRequest: span(parseCPU(input.cpuRequest)),
    memRequest: span(parseMemory(input.memRequest)),
    cpuLimit: span(parseCPU(input.cpuLimit)),
    memLimit: span(parseMemory(input.memLimit)),
  };
}
