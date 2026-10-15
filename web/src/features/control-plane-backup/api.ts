import { api, ApiError } from "@/lib/api-client";
import { env } from "@/lib/env";
import { verifyCompleteGzip } from "@/lib/gzip-verify";

// Limits enforced by server-go/internal/http/handlers/backup.go (Upload).
// The dump lands in a kube Secret, which caps out near 1 MiB.
export const RESTORE_MAX_BYTES = 900 * 1024;
export const RESTORE_MIN_BYTES = 32;
// Cluster-wide token bucket on POST /api/admin/restore.
export const RESTORE_RATE_BURST = 5;
export const RESTORE_CONFIRM_PHRASE = "restore control plane";

export type RestorePhase = "Running" | "Succeeded" | "Failed";

export interface RestoreStarted {
  jobName: string;
  secretName: string;
  statusUrl: string;
  hint: string;
}

export interface RestoreStatus {
  jobName: string;
  phase: RestorePhase;
  active: number;
  rolloutTriggered?: boolean;
  rolloutError?: string;
}

// validateRestoreFile checks what the browser can check before upload:
// the server's size window and the gzip magic bytes (the restore Job
// pipes the file through gunzip, so a plain .sql would fail in the pod).
export async function validateRestoreFile(file: Blob): Promise<string | null> {
  if (file.size < RESTORE_MIN_BYTES) return "File is empty or truncated.";
  if (file.size > RESTORE_MAX_BYTES) {
    return `File is ${formatKiB(file.size)}; the server accepts at most ${formatKiB(RESTORE_MAX_BYTES)}. Larger dumps need pg_restore run directly against kuso-postgres (docs/BACKUP_RESTORE.md).`;
  }
  const head = new Uint8Array(await file.slice(0, 2).arrayBuffer());
  if (head[0] !== 0x1f || head[1] !== 0x8b) {
    return "Not a gzip file. Upload the .sql.gz produced by the backup download (or `kuso backup`).";
  }
  return null;
}

export function formatKiB(bytes: number): string {
  return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KiB`;
}

async function errorFrom(res: Response): Promise<ApiError> {
  const text = await res.text().catch(() => "");
  let body: unknown = text;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    /* plain-text body */
  }
  return new ApiError(res.status, body, `${res.status} ${res.statusText}`);
}

function filenameFromDisposition(cd: string | null): string | null {
  if (!cd) return null;
  const m = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(cd);
  return m ? decodeURIComponent(m[1]) : null;
}

// downloadControlPlaneBackup fetches a fresh pg_dump of the kuso
// metadata DB and saves it. Buffered (not a plain <a href>) so a dump
// that dies mid-stream is caught by verifyCompleteGzip instead of being
// saved as a silently truncated file.
export async function downloadControlPlaneBackup(): Promise<{ filename: string; bytes: number }> {
  const res = await fetch(`${env.apiBase}/api/admin/backup`, { credentials: "include" });
  if (!res.ok) throw await errorFrom(res);
  const blob = await res.blob();
  await verifyCompleteGzip(blob);
  const filename =
    filenameFromDisposition(res.headers.get("Content-Disposition")) ?? "kuso-backup.sql.gz";
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
  return { filename, bytes: blob.size };
}

// startControlPlaneRestore uploads the raw gzip bytes (not multipart:
// the handler io.ReadAll's the body). api() would JSON-encode a Blob, so
// this goes through fetch directly.
export async function startControlPlaneRestore(file: Blob): Promise<RestoreStarted> {
  const res = await fetch(`${env.apiBase}/api/admin/restore`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/gzip" },
    body: file,
  });
  if (!res.ok) throw await errorFrom(res);
  return (await res.json()) as RestoreStarted;
}

// getRestoreStatus also drives the server side: the first poll that sees
// phase=Succeeded triggers the kuso-server rollout, so something must
// keep polling until a terminal phase.
export async function getRestoreStatus(jobName: string): Promise<RestoreStatus> {
  return api<RestoreStatus>(`/api/admin/restore/${encodeURIComponent(jobName)}`);
}

export function isTerminalPhase(p: RestorePhase | undefined): boolean {
  return p === "Succeeded" || p === "Failed";
}
