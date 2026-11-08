import { api, ApiError } from "@/lib/api-client";

// Mirrors builds.EnvExample in server-go/internal/builds/envexample.go.
export interface EnvExampleEntry {
  key: string;
  value: string;
  comment?: string;
}

export interface EnvExample {
  // "" when none of the checked paths exists.
  file: string;
  branch: string;
  checked: string[];
  entries: EnvExampleEntry[];
  truncated?: boolean;
}

export type EnvExampleResult = { kind: "ok"; example: EnvExample } | { kind: "unavailable" };

// A 404 that isn't about the project/service itself means the server
// predates the endpoint.
export function isMissingEndpoint(e: unknown): boolean {
  return e instanceof ApiError && e.status === 404 && !/^(project|service) /i.test(e.message);
}

export async function getEnvExample(project: string, service: string): Promise<EnvExampleResult> {
  try {
    const example = await api<EnvExample>(
      `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/env-example`,
    );
    return {
      kind: "ok",
      example: { ...example, checked: example.checked ?? [], entries: example.entries ?? [] },
    };
  } catch (e) {
    if (isMissingEndpoint(e)) return { kind: "unavailable" };
    throw e;
  }
}

export interface EnvExampleRow extends EnvExampleEntry {
  // Already set on the service: shown, never selectable, never written.
  exists: boolean;
}

// planEnvExampleImport marks which example keys the service already has
// (case-sensitive, like env names).
export function planEnvExampleImport(entries: EnvExampleEntry[], existing: ReadonlySet<string>): EnvExampleRow[] {
  return entries.map((e) => ({ ...e, exists: existing.has(e.key) }));
}

// pickEnvExampleEntries returns the selected keys that are still absent,
// so a key added between preview and apply is never overwritten.
export function pickEnvExampleEntries(
  rows: EnvExampleRow[],
  selected: ReadonlySet<string>,
  existing: ReadonlySet<string>,
): { name: string; value: string }[] {
  return rows
    .filter((r) => !r.exists && selected.has(r.key) && !existing.has(r.key))
    .map((r) => ({ name: r.key, value: r.value }));
}

export function envExampleQueryKey(project: string, service: string) {
  return ["projects", project, "services", service, "env-example"] as const;
}
