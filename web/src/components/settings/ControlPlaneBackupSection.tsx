"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { Download, Loader2, Upload, X } from "lucide-react";
import { SettingsField, SettingsSection } from "./SettingsSection";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import {
  RESTORE_CONFIRM_PHRASE,
  RESTORE_MAX_BYTES,
  RESTORE_RATE_BURST,
  downloadControlPlaneBackup,
  formatKiB,
  getRestoreStatus,
  isTerminalPhase,
  startControlPlaneRestore,
  validateRestoreFile,
  type RestorePhase,
} from "@/features/control-plane-backup";
import { cn } from "@/lib/utils";

// Survives a reload: the kuso-server rolls after a successful restore,
// and the rollout only fires once a status poll observes Succeeded.
const JOB_STORAGE_KEY = "kuso.controlPlaneRestoreJob";

const PHASE_TONE: Record<RestorePhase, string> = {
  Running: "bg-[var(--warning-subtle)] text-[var(--warning)]",
  Succeeded: "bg-[var(--success-subtle)] text-[var(--success)]",
  Failed: "bg-[var(--error-subtle)] text-[var(--error)]",
};

// ControlPlaneBackupSection: download a pg_dump of the kuso metadata DB
// and restore one. Admin-only (settings:admin); the caller gates render.
export function ControlPlaneBackupSection() {
  const [downloading, setDownloading] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [fileError, setFileError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [jobName, setJobName] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const saved = window.sessionStorage.getItem(JOB_STORAGE_KEY);
    if (saved) setJobName(saved);
  }, []);

  const trackJob = (name: string | null) => {
    setJobName(name);
    if (name) window.sessionStorage.setItem(JOB_STORAGE_KEY, name);
    else window.sessionStorage.removeItem(JOB_STORAGE_KEY);
  };

  const restore = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (f: File) => startControlPlaneRestore(f),
    onSuccess: (r) => {
      trackJob(r.jobName);
      setFile(null);
      if (fileInput.current) fileInput.current.value = "";
      toast.success(`Restore job ${r.jobName} started`);
    },
    onError: (e) => toastError(e, "Restore failed to start"),
  });

  const status = useQuery({
    queryKey: ["admin", "restore-status", jobName],
    queryFn: () => getRestoreStatus(jobName ?? ""),
    enabled: jobName !== null,
    // Keep polling through transient errors: kuso-server itself rolls
    // right after a successful restore.
    refetchInterval: (q) => (isTerminalPhase(q.state.data?.phase) ? false : 3000),
    retry: 5,
  });

  const onDownload = async () => {
    setDownloading(true);
    try {
      const r = await downloadControlPlaneBackup();
      toast.success(`Saved ${r.filename} (${formatKiB(r.bytes)})`);
    } catch (e) {
      toastError(e, "Backup download failed");
    } finally {
      setDownloading(false);
    }
  };

  const onPick = async (f: File | null) => {
    setFile(f);
    setFileError(f ? await validateRestoreFile(f) : null);
  };

  const running = jobName !== null && !isTerminalPhase(status.data?.phase);

  return (
    <SettingsSection
      title="Control-plane database"
      description="kuso's own metadata database: users, tokens, settings, build history and audit. Project data lives in addons and is not part of this dump."
    >
      <SettingsField
        label="Download backup"
        hint="A gzipped plain-SQL pg_dump (--clean --if-exists), taken now."
        aside={
          <Button size="sm" variant="outline" onClick={() => void onDownload()} disabled={downloading}>
            {downloading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Download className="h-3.5 w-3.5" />}
            {downloading ? "Dumping…" : "Download .sql.gz"}
          </Button>
        }
      />

      <div className="border-t border-[var(--border-subtle)] pt-5">
        <SettingsField
          label="Restore from backup"
          htmlFor="cp-restore-file"
          hint={
            <ul className="mt-1 list-disc space-y-1 pl-4">
              <li>
                Overwrites the whole control-plane database with the dump. Anything changed since the dump was taken
                is lost, including users and tokens.
              </li>
              <li>
                Gzipped pg_dump only, at most <span className="font-mono">{formatKiB(RESTORE_MAX_BYTES)}</span> (it is
                staged in a Kubernetes Secret). For bigger dumps, run pg_restore against kuso-postgres directly.
              </li>
              <li>
                Rate limit: {RESTORE_RATE_BURST} restores, then 1 per hour, cluster-wide. Failed attempts count.
              </li>
              <li>
                On success kuso-server restarts and you may need to sign in again. Keep this page open until the job
                finishes: the restart fires on the first status check that sees it succeed.
              </li>
            </ul>
          }
          error={fileError}
        >
          <div className="flex flex-wrap items-center gap-2">
            <input
              id="cp-restore-file"
              ref={fileInput}
              type="file"
              accept=".gz,application/gzip"
              onChange={(e) => void onPick(e.target.files?.[0] ?? null)}
              disabled={restore.isPending || running}
              className="min-w-0 max-w-full flex-1 text-xs text-[var(--text-secondary)] file:mr-2 file:rounded-sm file:border file:border-[var(--border-subtle)] file:bg-[var(--bg-primary)] file:px-2.5 file:py-1 file:text-xs file:text-[var(--text-primary)]"
            />
            {file && !fileError && (
              <span className="text-xs text-[var(--text-tertiary)]">{formatKiB(file.size)}</span>
            )}
            <Button
              size="sm"
              variant="destructive"
              disabled={!file || fileError !== null || restore.isPending || running}
              disabledReason={
                restore.isPending
                  ? undefined
                  : running
                    ? "A restore is already running"
                    : !file
                      ? "Choose a backup file (.gz) first"
                      : fileError !== null
                        ? "This file can't be restored: see the message below"
                        : undefined
              }
              onClick={() => setConfirming(true)}
            >
              <Upload className="h-3.5 w-3.5" />
              {restore.isPending ? "Uploading…" : "Restore…"}
            </Button>
          </div>
        </SettingsField>
        {running && (
          <p className="mt-2 text-xs text-[var(--text-tertiary)]">
            A restore is in progress; wait for it to finish before starting another.
          </p>
        )}

        {jobName && (
          <div className="mt-3 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2">
            <div className="flex items-center gap-2">
              <span className="min-w-0 flex-1 truncate font-mono text-xs">{jobName}</span>
              {status.data ? (
                <span className={cn("rounded px-1.5 py-0.5 text-xs font-medium", PHASE_TONE[status.data.phase])}>
                  {status.data.phase === "Running" && <Loader2 className="mr-1 inline h-3 w-3 animate-spin" />}
                  {status.data.phase}
                </span>
              ) : status.isPending ? (
                <span className="text-xs text-[var(--text-tertiary)]">Checking…</span>
              ) : null}
              {!running && (
                <button
                  type="button"
                  onClick={() => trackJob(null)}
                  aria-label="Dismiss restore status"
                  className="rounded p-0.5 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              )}
            </div>
            {status.data && (
              <p className="mt-1 text-xs text-[var(--text-secondary)]">
                {status.data.phase === "Running" &&
                  `${status.data.active} pod${status.data.active === 1 ? "" : "s"} active · polling every 3s`}
                {status.data.phase === "Succeeded" &&
                  (status.data.rolloutError
                    ? `Restored, but restarting kuso-server failed: ${status.data.rolloutError}. Run: kubectl -n kuso rollout restart deploy/kuso-server`
                    : "Restored. kuso-server is restarting; reload the page in a minute.")}
                {status.data.phase === "Failed" &&
                  `psql exited non-zero; the database may be partially restored. Inspect the job's pod logs in the kuso namespace (kept 7 days).`}
              </p>
            )}
            {status.isError && (
              <p className="mt-1 text-xs text-[var(--error)]">
                Status check failed: {status.error instanceof Error ? status.error.message : "unknown error"}
                {status.error instanceof Error && " (expected briefly while kuso-server restarts)"}
              </p>
            )}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirming}
        title="Restore the control-plane database?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">{file?.name}</span> replaces
            every table in kuso&apos;s metadata database. Users, tokens, settings and history
            created after this dump are gone. This can&apos;t be undone; download a fresh backup
            first if you might need the current state.
          </p>
        }
        typeToConfirm={RESTORE_CONFIRM_PHRASE}
        confirmLabel="Restore database"
        destructive
        pending={restore.isPending}
        onConfirm={() => {
          if (file) restore.mutate(file);
          setConfirming(false);
        }}
        onCancel={() => setConfirming(false)}
      />
    </SettingsSection>
  );
}
