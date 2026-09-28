import { api } from "@/lib/api-client";

export type DrainType = "http" | "otlp" | "loki";

// Drain mirrors one row of GET /api/drains. Header values and the
// secret come back masked; header names stay visible.
export interface Drain {
  id: string;
  name: string;
  type: DrainType;
  url: string;
  project?: string;
  headers?: Record<string, string>;
  secret?: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
  createdBy?: string;
}

export interface DrainInput {
  name?: string;
  type: DrainType;
  url: string;
  project?: string;
  headers?: Record<string, string>;
  secret?: string;
  enabled?: boolean;
}

export interface DrainTestResult {
  ok: boolean;
  status: number;
}

export async function listDrains(): Promise<Drain[]> {
  const out = await api<Drain[] | null>("/api/drains");
  return out ?? [];
}

// createDrain drops empty optional fields: an empty project would be
// read as "scoped to project ''" by anyone reading the payload.
export async function createDrain(input: DrainInput): Promise<Drain> {
  const body: DrainInput = { type: input.type, url: input.url };
  if (input.name) body.name = input.name;
  if (input.project) body.project = input.project;
  if (input.headers && Object.keys(input.headers).length > 0) body.headers = input.headers;
  if (input.secret) body.secret = input.secret;
  if (input.enabled !== undefined) body.enabled = input.enabled;
  return api<Drain>("/api/drains", { method: "POST", body });
}

export async function deleteDrain(id: string): Promise<void> {
  await api<void>(`/api/drains/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function testDrain(id: string): Promise<DrainTestResult> {
  return api<DrainTestResult>(`/api/drains/${encodeURIComponent(id)}/test`, { method: "POST" });
}

export type ParsedHeaders = { ok: true; headers: Record<string, string> } | { ok: false; error: string };

// parseHeaderLines reads one header per line as "Name: value" or
// "Name=value", splitting on the first separator so values like
// "Bearer a=b" survive.
export function parseHeaderLines(text: string): ParsedHeaders {
  const headers: Record<string, string> = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const idx = line.search(/[:=]/);
    if (idx <= 0) return { ok: false, error: `"${line}" — use Name: value` };
    headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim();
  }
  return { ok: true, headers };
}
