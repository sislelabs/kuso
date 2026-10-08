import { api } from "@/lib/api-client";

// The server MERGES `settings` into the Kuso CR spec by top-level key:
// omitted keys are kept and only a null deletes one.
export async function updateClusterSettings(settings: Record<string, unknown>): Promise<void> {
  await api("/api/config", { method: "POST", body: { settings } });
}

// settingsPatch is `next` plus a null for every top-level key that was
// in the loaded spec but is gone from `next`, so a key deleted in the
// editor is actually removed rather than silently kept by the merge.
export function settingsPatch(
  loaded: Record<string, unknown>,
  next: Record<string, unknown>,
): Record<string, unknown> {
  const patch: Record<string, unknown> = { ...next };
  for (const k of Object.keys(loaded)) {
    if (!(k in next)) patch[k] = null;
  }
  return patch;
}

// Pod-size preset as the server serialises it (untagged Go struct, so the
// keys are capitalised).
export interface PodSize {
  ID: string;
  Name: string;
  CPURequest: string;
  CPULimit: string;
  MemoryRequest: string;
  MemoryLimit: string;
}

// "none" = new services get no requests/limits.
export const POD_SIZE_NONE = "none";

export async function listPodSizes(): Promise<PodSize[]> {
  return (await api<PodSize[] | null>("/api/config/podsizes")) ?? [];
}

export async function getDefaultPodSize(): Promise<string> {
  return (await api<{ name: string }>("/api/config/default-podsize")).name;
}

export async function setDefaultPodSize(name: string): Promise<void> {
  await api("/api/config/default-podsize", { method: "PUT", body: { name } });
}
