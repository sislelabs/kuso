import { api, ApiError } from "@/lib/api-client";
import { env } from "@/lib/env";
import { verifyCompleteGzip } from "@/lib/gzip-verify";
import { relativeTime } from "@/lib/format";
import type { BackupObject } from "@/features/projects";
import type { KusoAddon } from "@/types/projects";

const base = (project: string, addon: string) =>
  `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/backups`;

export const addonBackupsQueryKey = (project: string, addon: string) =>
  ["addons", project, addon, "backups"] as const;

// isBackupsNotConfigured spots the list endpoint's "S3 not set up" reply
// (503 + "not configured"). Any other 503 is a real outage.
export function isBackupsNotConfigured(e: unknown): boolean {
  return e instanceof ApiError && e.status === 503 && /not configured/i.test(e.message);
}

// What the backups list query knows about S3, as the rules below need it.
export type BackupsListState =
  | { kind: "loading" }
  | { kind: "ok"; items: BackupObject[] }
  | { kind: "no-s3" }
  | { kind: "error"; message: string };

export function backupsListState(q: { isPending: boolean; isError: boolean; error: unknown; data?: BackupObject[] }): BackupsListState {
  if (q.isError) {
    if (isBackupsNotConfigured(q.error)) return { kind: "no-s3" };
    return { kind: "error", message: q.error instanceof Error ? q.error.message : "load failed" };
  }
  if (q.isPending || !q.data) return { kind: "loading" };
  return { kind: "ok", items: q.data };
}

// backupsApplicable: only postgres addons have managed backups today.
export function backupsApplicable(cr: KusoAddon | undefined): boolean {
  return cr?.spec.kind === "postgres";
}

export type BackupNowState = { enabled: boolean; reason: string };

// backupNowState is the single rule for the "Back up now" action. It
// mirrors when the server's POST .../backups/run can succeed: the chart
// only renders the backup CronJob for a scheduled, kuso-managed,
// non-shared postgres, and the job needs S3 to land anywhere.
export function backupNowState(
  cr: KusoAddon | undefined,
  opts: { canWrite: boolean; list: BackupsListState },
): BackupNowState {
  const spec = cr?.spec;
  if (!spec) return { enabled: false, reason: "Loading the addon…" };
  if (spec.kind !== "postgres") return { enabled: false, reason: `kuso doesn't back up ${spec.kind || "this kind of"} addons` };
  if (spec.external?.secretName) return { enabled: false, reason: "External database: back it up with its provider" };
  if (spec.useInstanceAddon) {
    return {
      enabled: false,
      reason: `This database lives on the shared instance database ${spec.useInstanceAddon}, which backs it up on its own schedule`,
    };
  }
  if (!opts.canWrite) return { enabled: false, reason: "Requires editor access on this project" };
  if (!spec.backup?.schedule?.trim()) return { enabled: false, reason: "Turn on a backup schedule in the Backups tab first" };
  if (opts.list.kind === "no-s3") return { enabled: false, reason: "Add S3 credentials in Settings → Backups first" };
  // A list query with no data yet flips back to pending on every refetch,
  // so "loading" must block too or the button flickers enabled.
  if (opts.list.kind === "loading") return { enabled: false, reason: "Checking backup storage…" };
  return { enabled: true, reason: "Run the scheduled backup now: dump, upload to S3, prune old ones" };
}

// describeCron turns the common backup schedules into words; anything
// unusual falls back to the raw expression.
export function describeCron(expr: string): string {
  const f = expr.trim().split(/\s+/);
  if (f.length !== 5) return expr;
  const [min, hour, dom, mon, dow] = f;
  const at = /^\d+$/.test(min) && /^\d+$/.test(hour) ? ` at ${hour.padStart(2, "0")}:${min.padStart(2, "0")}` : "";
  if (dom === "*" && mon === "*" && dow === "*" && at) return `daily${at}`;
  if (dom === "*" && mon === "*" && /^\d$/.test(dow) && at) {
    const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
    return `weekly on ${days[Number(dow) % 7]}${at}`;
  }
  if (/^\*\/\d+$/.test(hour) && dom === "*" && mon === "*" && dow === "*" && /^\d+$/.test(min)) return `every ${hour.slice(2)}h`;
  return expr;
}

export type BackupSummary = {
  // e.g. "Scheduled daily at 03:00", "Scheduled backups off", "No backups yet"
  headline: string;
  // e.g. "Last backup 4d ago", "Scheduled backups off"
  detail: string;
  // headline + detail as one line, for tooltips and the Backups tab.
  sentence: string;
};

function newest(items: BackupObject[]): BackupObject | undefined {
  let best: BackupObject | undefined;
  for (const b of items) if (b.when && (!best || b.when > best.when)) best = b;
  return best;
}

// backupSummary is the one wording of "is this addon backed up" shared by
// the Overview tile and the Backups tab, so the two can't disagree.
export function backupSummary(cr: KusoAddon | undefined, list: BackupsListState, now: number = Date.now()): BackupSummary {
  const schedule = cr?.spec.backup?.schedule?.trim() ?? "";
  const shared = cr?.spec.useInstanceAddon;
  const plan = shared
    ? `Backed up with ${shared}`
    : schedule
      ? `Scheduled ${describeCron(schedule)}`
      : "Scheduled backups off";
  const make = (headline: string, detail: string): BackupSummary => ({
    headline,
    detail,
    // Lower-case the joined detail's first letter, but not an acronym's ("S3").
    sentence: detail ? `${headline} · ${/^[A-Z][a-z]/.test(detail) ? detail.charAt(0).toLowerCase() + detail.slice(1) : detail}` : headline,
  });
  switch (list.kind) {
    case "loading":
      return make(plan, "");
    case "no-s3":
      return make(plan, "S3 isn't set up");
    case "error":
      return make(plan, "Couldn't list backups");
    case "ok": {
      const last = newest(list.items);
      if (!last) return schedule || shared ? make(plan, "No backups yet") : make("No backups yet", plan);
      return make(plan, `Last backup ${relativeTime(last.when, now)}`);
    }
  }
}

// runBackupNow starts a one-off run of the addon's scheduled backup job
// (same dump + upload + prune as the CronJob). 409 = no schedule set.
export async function runBackupNow(project: string, addon: string): Promise<{ job: string }> {
  try {
    return await api<{ job: string }>(`${base(project, addon)}/run`, { method: "POST" });
  } catch (e) {
    if (e instanceof ApiError && (e.status === 404 || e.status === 405)) {
      throw new Error("This kuso server can't start a backup on demand yet. Upgrade kuso, or download a fresh dump instead.");
    }
    throw e;
  }
}

// downloadStoredBackup saves one stored backup object from the bucket.
// Admin-only and audited server-side (it's the whole dataset).
export async function downloadStoredBackup(project: string, addon: string, key: string): Promise<void> {
  const res = await fetch(`${env.apiBase}${base(project, addon)}/object?key=${encodeURIComponent(key)}`, {
    credentials: "include",
  });
  if (!res.ok) {
    if (res.status === 404 && !(res.headers.get("content-type") ?? "").includes("json")) {
      throw new Error("This kuso server can't download stored backups yet. Upgrade kuso to enable it.");
    }
    const detail = await res.text().catch(() => "");
    throw new Error(errorText(detail) || `${res.status} ${res.statusText}`);
  }
  const blob = await res.blob();
  if (key.endsWith(".gz")) await verifyCompleteGzip(blob);
  saveBlob(blob, key.slice(key.lastIndexOf("/") + 1) || "backup");
}

function errorText(body: string): string {
  try {
    const j = JSON.parse(body) as { error?: unknown };
    if (typeof j.error === "string") return j.error;
  } catch {
    /* plain text body */
  }
  return body.trim();
}

function saveBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
