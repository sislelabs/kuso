// Pod size + replica fields on the add-service form. The server's create
// DTO has no `size` field, so (like the CLI's --size) the preset is
// resolved client-side into an explicit resources block.

export interface SizingPreset {
  Name: string;
  CPURequest: string;
  CPULimit: string;
  MemoryRequest: string;
  MemoryLimit: string;
}

export interface SizingInput {
  // "" = the instance default pod size; "none" = no requests/limits.
  size: string;
  replicas: string;
  maxReplicas: string;
}

export interface SizingBody {
  resources?: Record<string, Record<string, string>>;
  scale?: { min?: number; max?: number };
}

export const SIZE_NONE = "none";
const DEFAULT_MAX = 5;

export function presetResources(p: SizingPreset): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  const put = (section: string, key: string, v: string) => {
    if (!v.trim()) return;
    (out[section] ??= {})[key] = v.trim();
  };
  put("requests", "cpu", p.CPURequest);
  put("requests", "memory", p.MemoryRequest);
  put("limits", "cpu", p.CPULimit);
  put("limits", "memory", p.MemoryLimit);
  return out;
}

export function sizingBody(input: SizingInput, presets: SizingPreset[]): SizingBody | { error: string } {
  const body: SizingBody = {};
  if (input.size === SIZE_NONE) {
    body.resources = {};
  } else if (input.size) {
    const p = presets.find((x) => x.Name === input.size);
    if (!p) return { error: `Unknown pod size "${input.size}"` };
    body.resources = presetResources(p);
  }
  const min = input.replicas.trim() ? Number(input.replicas) : undefined;
  const max = input.maxReplicas.trim() ? Number(input.maxReplicas) : undefined;
  if (min !== undefined && (!Number.isInteger(min) || min < 1)) return { error: "Replicas must be a whole number ≥ 1" };
  if (max !== undefined && (!Number.isInteger(max) || max < 1)) return { error: "Max replicas must be a whole number ≥ 1" };
  if (min !== undefined && max !== undefined && max < min) return { error: "Max replicas can't be below replicas" };
  if (min !== undefined || max !== undefined) {
    body.scale = {};
    if (min !== undefined) body.scale.min = min;
    // Mirrors the CLI: max defaults to 5, or to replicas when higher.
    const effMax = max ?? (min !== undefined && min > DEFAULT_MAX ? min : undefined);
    if (effMax !== undefined) body.scale.max = effMax;
  }
  return body;
}
