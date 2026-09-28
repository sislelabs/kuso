import { api } from "@/lib/api-client";

// The server replaces the whole Kuso CR spec with `settings`, so callers
// must send the full spec they loaded, not just the edited keys.
export async function updateClusterSettings(settings: Record<string, unknown>): Promise<void> {
  await api("/api/config", { method: "POST", body: { settings } });
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
