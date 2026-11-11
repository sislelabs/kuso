import { api, ApiError } from "@/lib/api-client";

// A row from the Postgres log archive (/logs/search).
export interface ArchiveLine {
  id: number;
  ts: string;
  pod: string;
  line: string;
  project?: string;
  service?: string;
  env?: string;
}

export interface ArchivePage {
  lines: ArchiveLine[];
  nextBeforeId?: number;
  // Echoed by servers that honour the afterId forward cursor.
  afterId?: number;
  // How far back the archive really goes; the byte budget can cut it
  // well short of the configured days. Only on the newest page.
  retention?: { oldestTs?: string; configuredDays: number };
}

// LogScope selects the per-service or the project-wide search endpoint.
export type LogScope = { project: string; service?: string };

export interface ArchiveParams {
  q?: string;
  env?: string;
  since?: string;
  until?: string;
  limit?: number;
  beforeId?: number;
  afterId?: number;
}

function searchPath(scope: LogScope): string {
  const p = encodeURIComponent(scope.project);
  return scope.service
    ? `/api/projects/${p}/services/${encodeURIComponent(scope.service)}/logs/search`
    : `/api/projects/${p}/logs/search`;
}

export async function searchArchive(scope: LogScope, params: ArchiveParams): Promise<ArchivePage> {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "" && v !== 0) sp.set(k, String(v));
  }
  const qs = sp.toString();
  const res = await api<ArchivePage>(`${searchPath(scope)}${qs ? "?" + qs : ""}`);
  return { ...res, lines: res.lines ?? [] };
}

export interface LogAnchor {
  id?: number;
  ts: string;
  pod: string;
  line: string;
}

export interface LogContext {
  // Oldest-first, anchor included.
  lines: ArchiveLine[];
  anchorIndex: number;
}

const CONTEXT = 50;

// fetchLogContext returns up to 50 lines either side of an anchor,
// unfiltered (no query), from the archive. The anchor may come from the
// live tail (no archive id): it's then located by pod + text at its
// timestamp. Servers without the afterId cursor fall back to a short
// time window after the anchor.
export async function fetchLogContext(scope: LogScope, env: string | undefined, anchor: LogAnchor): Promise<LogContext> {
  const anchorMs = Date.parse(anchor.ts);
  if (Number.isNaN(anchorMs)) return { lines: [], anchorIndex: -1 };
  // `until` is exclusive; +1ms includes lines stamped at the anchor.
  const before = await searchArchive(scope, {
    env,
    until: new Date(anchorMs + 1).toISOString(),
    limit: CONTEXT + 20,
    beforeId: anchor.id ? anchor.id + 1 : undefined,
  });
  const older = [...before.lines].reverse();
  let idx = -1;
  if (anchor.id) idx = older.findIndex((l) => l.id === anchor.id);
  if (idx < 0) {
    for (let i = older.length - 1; i >= 0; i--) {
      if (older[i].pod === anchor.pod && older[i].line === anchor.line) {
        idx = i;
        break;
      }
    }
  }
  const anchorId = idx >= 0 ? older[idx].id : older[older.length - 1]?.id;
  const head = idx >= 0 ? older.slice(Math.max(0, idx - CONTEXT), idx + 1) : older.slice(-CONTEXT);
  if (!anchorId) return { lines: head, anchorIndex: idx >= 0 ? head.length - 1 : -1 };

  let after: ArchiveLine[] = [];
  const fwd = await searchArchive(scope, { env, afterId: anchorId, limit: CONTEXT });
  if (fwd.afterId === anchorId) {
    after = [...fwd.lines].reverse();
  } else {
    const win = await searchArchive(scope, {
      env,
      since: new Date(anchorMs).toISOString(),
      until: new Date(anchorMs + 60_000).toISOString(),
      limit: 500,
    });
    after = win.lines.filter((l) => l.id > anchorId).sort((a, b) => a.id - b.id).slice(0, CONTEXT);
  }
  return { lines: [...head, ...after], anchorIndex: idx >= 0 ? head.length - 1 : -1 };
}

// ---- errors ---------------------------------------------------------------

export interface ErrorOccurrence {
  ts: string;
  env?: string;
  pod?: string;
  rawLine: string;
}

export interface ErrorGroupDetail {
  fingerprint: string;
  bucketSeconds: number;
  buckets: [number, number][];
  occurrences: ErrorOccurrence[];
}

// getErrorDetail returns null when the server predates the endpoint, so
// the Errors tab degrades to the group summary instead of an error state.
export async function getErrorDetail(
  project: string,
  service: string,
  fingerprint: string,
  since: string,
  env?: string,
): Promise<ErrorGroupDetail | null> {
  const sp = new URLSearchParams({ since });
  if (env) sp.set("env", env);
  try {
    return await api<ErrorGroupDetail>(
      `/api/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/errors/${encodeURIComponent(fingerprint)}?${sp}`,
    );
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) return null;
    throw e;
  }
}

// fetchErrorTrace pulls the lines a pod logged right after an error
// occurrence (same pod, within 2s) — the stack trace the scanner's
// one-line sample cuts off.
export async function fetchErrorTrace(
  scope: LogScope,
  occ: { ts: string; pod?: string; env?: string },
): Promise<ArchiveLine[]> {
  const ms = Date.parse(occ.ts);
  if (Number.isNaN(ms)) return [];
  const page = await searchArchive(scope, {
    env: occ.env,
    since: new Date(ms - 1).toISOString(),
    until: new Date(ms + 2_000).toISOString(),
    limit: 200,
  });
  return page.lines.filter((l) => !occ.pod || l.pod === occ.pod).sort((a, b) => a.id - b.id);
}
