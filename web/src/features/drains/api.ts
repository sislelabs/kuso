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

// The server masks stored header values and the secret with this
// sentinel on read, and treats it as "keep what's stored" on update.
export const DRAIN_MASK = "••••••••";

export interface DrainEditForm {
  name: string;
  type: DrainType;
  url: string;
  project: string;
  headersText: string;
  // Blank = keep the stored secret.
  secret: string;
  clearSecret: boolean;
  enabled: boolean;
}

export function drainToEditForm(d: Drain): DrainEditForm {
  return {
    name: d.name,
    type: d.type,
    url: d.url,
    project: d.project ?? "",
    headersText: Object.entries(d.headers ?? {})
      .map(([k, v]) => `${k}: ${v}`)
      .join("\n"),
    secret: "",
    clearSecret: false,
    enabled: d.enabled,
  };
}

export type DrainUpdateBody = Required<Omit<DrainInput, "name">> & { name: string };

// buildDrainUpdate turns the edit form into the PUT body. PUT replaces
// every field, so unchanged ones are sent as-is; masked header values and
// an untouched secret go back as the sentinel so the server keeps them.
export function buildDrainUpdate(
  d: Drain,
  f: DrainEditForm
): { ok: true; body: DrainUpdateBody } | { ok: false; error: string } {
  const parsed = parseHeaderLines(f.headersText);
  if (!parsed.ok) return { ok: false, error: `Header ${parsed.error}` };
  let secret = "";
  if (f.secret) secret = f.secret;
  else if (!f.clearSecret && d.secret) secret = DRAIN_MASK;
  return {
    ok: true,
    body: {
      name: f.name.trim(),
      type: f.type,
      url: f.url.trim(),
      project: f.project.trim(),
      headers: parsed.headers,
      secret,
      enabled: f.enabled,
    },
  };
}

export async function updateDrain(id: string, body: DrainUpdateBody): Promise<Drain> {
  return api<Drain>(`/api/drains/${encodeURIComponent(id)}`, { method: "PUT", body });
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
