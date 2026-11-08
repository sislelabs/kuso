import { api, ApiError } from "@/lib/api-client";
import { env } from "@/lib/env";
import { verifyCompleteGzip } from "@/lib/gzip-verify";

const base = (project: string, addon: string) =>
  `/api/projects/${encodeURIComponent(project)}/addons/${encodeURIComponent(addon)}/backups`;

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
