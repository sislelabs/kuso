import { api } from "@/lib/api-client";

// Runtime config knobs (stored in the Setting kv, hot-reloaded by the agent
// Manager). Mirrors db.IncidentAgentConfig.
export interface IncidentAgentConfig {
  enabled: boolean;
  triggerPod: boolean;
  triggerAlert: boolean;
  triggerNode: boolean;
  maxConcurrent: number;
  cooldownHours: number;
  agentImage?: string;
}

// Computed, NON-secret status. The server never returns secret values —
// only presence + safe metadata.
export interface IncidentAgentStatus {
  ccConfigured: boolean;
  ccExpiresAt?: string;
  ccSubscriptionType?: string;
  discordConfigured: boolean;
  channelId?: string;
  botDeployed: boolean;
  botReady: boolean;
  openIncidents: number;
}

export interface IncidentAgentSettings {
  config: IncidentAgentConfig;
  status: IncidentAgentStatus;
}

const BASE = "/api/admin/settings/incident-agent";

export async function getIncidentAgentSettings(): Promise<IncidentAgentSettings> {
  return api<IncidentAgentSettings>(BASE);
}

export async function putIncidentAgentConfig(cfg: IncidentAgentConfig): Promise<void> {
  await api(BASE, { method: "PUT", body: cfg });
}

// Write-only: the credentials JSON blob (the claudeAiOauth shape). The server
// validates + stores it; it's never echoed back.
export async function putCCCredentials(credentials: string): Promise<void> {
  await api(`${BASE}/cc-credentials`, { method: "PUT", body: { credentials } });
}

// Write-only Discord config; each field optional (only-set fields update).
export async function putDiscordConfig(body: {
  botToken?: string;
  kusoBotToken?: string;
  channelId?: string;
}): Promise<void> {
  await api(`${BASE}/discord`, { method: "PUT", body });
}
