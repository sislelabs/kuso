import { api } from "@/lib/api-client";

// The server replaces the whole Kuso CR spec with `settings`, so callers
// must send the full spec they loaded, not just the edited keys.
export async function updateClusterSettings(settings: Record<string, unknown>): Promise<void> {
  await api("/api/config", { method: "POST", body: { settings } });
}
