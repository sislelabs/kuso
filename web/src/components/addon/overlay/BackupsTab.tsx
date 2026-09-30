"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { RotateCcw, Download } from "lucide-react";
import {
  useAddons,
  listBackups,
  restoreBackup,
  downloadAddonBackup,
  updateAddon,
  type BackupObject,
} from "@/features/projects";
import type { KusoAddon } from "@/types/projects";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { CronPicker } from "@/components/shared/CronPicker";
import { useCanOnProject, Perms } from "@/features/auth";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";

// addonCRName + addonShort mirror the server's CRName/ShortName
// helpers so we can map between "<project>-<addon>" CR names and the
// short user-facing names.
function addonCRName(project: string, addon: string): string {
  return addon.startsWith(project + "-") ? addon : `${project}-${addon}`;
}
function addonShort(project: string, name: string): string {
  const prefix = project + "-";
  return name.startsWith(prefix) ? name.slice(prefix.length) : name;
}

function tail(key: string): string {
  const i = key.lastIndexOf("/");
  return i >= 0 ? key.slice(i + 1) : key;
}

function formatBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(v >= 100 ? 0 : 1) + " " + units[i];
}

// DownloadBackupButton triggers an on-demand dump of the addon straight
// to the browser — no S3 config required. Rendered in every branch of the
// tab (including the "backups not set up" error state), since its whole
// point is to work when scheduled backups aren't configured. Only shown
// for kinds the server can dump: postgres + s3.
function DownloadBackupButton({
  project,
  addon,
  kind,
}: {
  project: string;
  addon: string;
  kind?: string;
}) {
  const [busy, setBusy] = useState(false);
  if (kind !== "postgres" && kind !== "s3" && kind !== "minio") return null;
  return (
    <Button
      size="sm"
      variant="outline"
      disabled={busy}
      onClick={async () => {
        setBusy(true);
        try {
          await downloadAddonBackup(project, addon);
        } catch (e) {
          toast.error(e instanceof Error ? e.message : "Download failed");
        } finally {
          setBusy(false);
        }
      }}
    >
      <Download className="h-3 w-3" />
      {busy ? "Preparing…" : "Download backup now"}
    </Button>
  );
}

export function BackupsTab({ project, addon }: { project: string; addon: string }) {
  const qc = useQueryClient();
  // One useAddons call drives both the schedule editor (this addon's
  // current backup config) and the cross-restore picker (sibling
  // postgres addons). Avoid double-fetching the same query key.
  const allAddons = useAddons(project);
  const thisAddon = (allAddons.data ?? []).find(
    (a) => a.metadata.name === addonCRName(project, addon),
  );
  const list = useQuery({
    queryKey: ["addons", project, addon, "backups"],
    queryFn: () => listBackups(project, addon),
    refetchInterval: 30_000,
  });
  // Restore + schedule edits are addons:write mutations; viewers get
  // the buttons disabled with a hint instead of a post-click 403.
  const canWrite = useCanOnProject(project, Perms.AddonsWrite);
  const restore = useMutation({
    mutationFn: ({ key, into, confirm }: { key: string; into?: string; confirm?: string }) =>
      // In-place restore (no `into`) is destructive; the server requires
      // `confirm` to echo the addon name. We forward exactly what the
      // user typed in the dialog — auto-supplying it here would defeat
      // the server's type-the-name safeguard.
      restoreBackup(project, addon, key, into, confirm),
    onSuccess: () => {
      toast.success("Restore started");
      qc.invalidateQueries({ queryKey: ["addons", project, addon, "backups"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Restore failed"),
  });
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  // Sibling postgres addons in the project — used as cross-restore
  // targets (non-destructive path). Postgres-only because the
  // existing restore Job assumes pg_dump format.
  const siblingAddons = (allAddons.data ?? []).filter(
    (a) => a.metadata.name !== addonCRName(project, addon) && a.spec.kind === "postgres",
  );

  if (list.isPending) {
    return <Skeleton className="m-5 h-40" />;
  }
  if (list.isError) {
    const msg = list.error instanceof Error ? list.error.message : "load failed";
    // 503 is the server's "S3 not configured" signal. Anything else
    // means the bucket is reachable but something is wrong (auth,
    // permissions, network) — those need a different message.
    const noS3 = msg.includes("503") || /s3|bucket|credentials/i.test(msg);
    return (
      <div className="space-y-4 p-5">
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
        <div className="flex items-center justify-between">
          <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
          <DownloadBackupButton project={project} addon={addon} kind={thisAddon?.spec.kind} />
        </div>
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          {noS3 ? "Backups not set up yet" : `Backups unavailable: ${msg}`}
          <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
            {noS3 ? (
              <>
                S3 credentials live in{" "}
                <a href="/settings/backups" className="text-[var(--accent)] underline">
                  Settings → Backups
                </a>{" "}
                (admin). Once they are set, pick a schedule above.
              </>
            ) : (
              <>
                Detail:{" "}
                <span className="font-mono text-[var(--text-secondary)]">{msg}</span>
              </>
            )}
          </p>
        </div>
      </div>
    );
  }

  const items = list.data ?? [];
  if (items.length === 0) {
    return (
      <div className="space-y-4 p-5">
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
        <div className="flex items-center justify-between">
          <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
          <DownloadBackupButton project={project} addon={addon} kind={thisAddon?.spec.kind} />
        </div>
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          No backups yet. The CronJob will drop one once its schedule fires.
        </p>
      </div>
    );
  }
  // Newest first.
  const sorted = [...items].sort((a, b) => (b.when ?? "").localeCompare(a.when ?? ""));

  return (
    <div className="space-y-4 p-5">
      <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
      <header className="mb-3 flex items-center justify-between gap-3">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
        <div className="flex items-center gap-3">
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {items.length} {items.length === 1 ? "object" : "objects"} · auto-refresh 30s
          </span>
          <DownloadBackupButton project={project} addon={addon} kind={thisAddon?.spec.kind} />
        </div>
      </header>
      <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {sorted.map((b) => (
          <li
            key={b.key}
            className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-3 py-2 last:border-b-0"
          >
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[12px] text-[var(--text-secondary)]">
                {tail(b.key)}
              </div>
              <div className="mt-0.5 flex items-center gap-3 font-mono text-[10px] text-[var(--text-tertiary)]">
                <span>{formatBytes(b.size)}</span>
                <span>·</span>
                <span title={b.when}>{b.when ? relativeTime(b.when) : "—"}</span>
              </div>
            </div>
            <Button
              size="sm"
              variant="outline"
              disabled={restore.isPending || !canWrite}
              title={canWrite ? undefined : "Requires editor access on this project"}
              onClick={() => setConfirmKey(b.key)}
            >
              <RotateCcw className="h-3 w-3" />
              Restore
            </Button>
          </li>
        ))}
      </ul>
      <ConfirmRestore
        item={sorted.find((b) => b.key === confirmKey) ?? null}
        pending={restore.isPending}
        sourceAddon={addon}
        siblings={siblingAddons.map((a) => addonShort(project, a.metadata.name))}
        onCancel={() => setConfirmKey(null)}
        onConfirm={(key, into, confirm) => {
          restore.mutate({ key, into, confirm });
          setConfirmKey(null);
        }}
      />
    </div>
  );
}

function ConfirmRestore({
  item,
  pending,
  sourceAddon,
  siblings,
  onCancel,
  onConfirm,
}: {
  item: BackupObject | null;
  pending: boolean;
  sourceAddon: string;
  siblings: string[];
  onCancel: () => void;
  onConfirm: (key: string, into?: string, confirm?: string) => void;
}) {
  const [target, setTarget] = useState<string>("");
  const inPlace = target === "";
  // Reset to in-place every time a new backup is picked so the user
  // explicitly opts into the cross-addon path each time.
  useEffect(() => {
    if (item) setTarget("");
  }, [item]);
  return (
    <ConfirmDialog
      open={item !== null}
      title="Restore this backup?"
      body={
        item && (
          <div className="space-y-2">
            <p>
              Loads <span className="font-mono">{tail(item.key)}</span> into:
            </p>
            <select
              id="restore-target"
              aria-label="Restore into"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              className="block w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1.5 font-mono text-[12px]"
            >
              <option value="">{sourceAddon} (overwrite)</option>
              {siblings.map((s) => (
                <option key={s} value={s}>
                  {s} ({sourceAddon} untouched)
                </option>
              ))}
            </select>
            {inPlace ? (
              <p className="text-[var(--error)]">Existing tables in {sourceAddon} are overwritten.</p>
            ) : (
              <p className="text-[var(--text-tertiary)]">{target} must be an existing postgres addon.</p>
            )}
          </div>
        )
      }
      confirmLabel={`Restore into ${target || sourceAddon}`}
      // The server re-checks the typed name for the in-place path.
      typeToConfirm={inPlace ? sourceAddon : undefined}
      destructive={inPlace}
      pending={pending}
      onConfirm={() => {
        if (item) onConfirm(item.key, target || undefined, inPlace ? sourceAddon : undefined);
      }}
      onCancel={onCancel}
    />
  );
}

// 5-field cron regex mirrors the server's addons.cronExpr5. Pre-flight
// check so a typo (in CronPicker's Custom mode) turns the Save button
// into a visible warning rather than a 400 toast after the round-trip.
// Inside character classes \* and \/ are unnecessary escapes — eslint
// flagged them. The hyphen is at the end of the class so it doesn't
// need escaping either; keep classes spelled out for readability.
const CRON_RE = /^[\d*/,?-]+\s+[\d*/,?-]+\s+[\d*/,?-]+\s+[\d*/,?-]+\s+[\d*/,?-]+$/;

// BackupScheduleEditor lets the user enable / change / disable the
// per-addon backup CronJob. Schedule = cron expression; retentionDays
// = N days after which the cronjob's prune step deletes old objects
// (0 = keep forever). PATCHes the addon CR via /api/.../addons/{a}.
function BackupScheduleEditor({
  project,
  addon,
  thisAddon,
  canWrite,
}: {
  project: string;
  addon: string;
  thisAddon?: KusoAddon;
  // addons:write gate, resolved by the parent tab. Saving the
  // schedule PATCHes the addon CR — viewers see the current config
  // but can't submit changes.
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const initialSchedule = thisAddon?.spec.backup?.schedule ?? "";
  const initialRetention = thisAddon?.spec.backup?.retentionDays ?? 14;
  const [schedule, setSchedule] = useState(initialSchedule);
  const [retention, setRetention] = useState(String(initialRetention));

  // Re-baseline when the parent addon refetches (e.g. after a save
  // returns and refreshes the cache). Without this the inputs would
  // hold the user's pre-save edits forever.
  useEffect(() => {
    setSchedule(initialSchedule);
    setRetention(String(initialRetention));
  }, [initialSchedule, initialRetention]);

  const trimmed = schedule.trim();
  const scheduleValid = trimmed === "" || CRON_RE.test(trimmed);
  const retentionNum = Number(retention);
  const retentionValid =
    Number.isInteger(retentionNum) && retentionNum >= 0 && retentionNum <= 3650;

  const dirty =
    trimmed !== initialSchedule.trim() || retentionNum !== initialRetention;
  const enabled = trimmed !== "";

  const save = useMutation({
    mutationFn: () =>
      updateAddon(project, addon, {
        backup: { schedule: trimmed, retentionDays: retentionNum },
      }),
    onSuccess: () => {
      toast.success(
        trimmed === ""
          ? "Backups disabled"
          : `Schedule saved: ${trimmed} (retain ${retentionNum}d)`,
      );
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
      qc.invalidateQueries({ queryKey: ["addons", project, addon, "backups"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  return (
    <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <header className="mb-3 flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Schedule</h3>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {enabled ? "active" : "disabled"}
        </span>
      </header>

      <div className="space-y-3">
        <div className="space-y-2">
          <label className="flex items-center gap-2 text-[12px]">
            <input
              type="checkbox"
              checked={trimmed !== ""}
              onChange={(e) => {
                if (e.target.checked) {
                  // Re-enabling: hand back the user's last good
                  // schedule, or fall back to a sensible daily-3am
                  // default if they're enabling for the first time.
                  setSchedule(initialSchedule || "0 3 * * *");
                } else {
                  setSchedule("");
                }
              }}
              className="h-3.5 w-3.5 cursor-pointer accent-[var(--accent)]"
            />
            <span>Run backups on a schedule</span>
          </label>
          {trimmed !== "" && (
            <CronPicker value={schedule} onChange={setSchedule} />
          )}
          {!scheduleValid && (
            <p className="font-mono text-[10px] text-red-400">
              Custom cron must be 5 fields (e.g. <code>0 3 * * *</code>).
            </p>
          )}
        </div>

        <div className="space-y-1">
          <label className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Retention (days)
          </label>
          <Input
            type="number"
            min={0}
            max={3650}
            value={retention}
            onChange={(e) => setRetention(e.target.value)}
            className={cn(
              "h-8 w-24 font-mono text-[12px]",
              !retentionValid && "border-red-500/60",
            )}
          />
          <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {retentionNum === 0
              ? "Keep forever (no automatic prune)."
              : `Backups older than ${retentionNum}d are deleted from S3.`}
          </p>
        </div>

        <div className="flex items-center justify-end gap-2 pt-1">
          {dirty && (
            <Button
              variant="ghost"
              size="sm"
              type="button"
              onClick={() => {
                setSchedule(initialSchedule);
                setRetention(String(initialRetention));
              }}
              disabled={save.isPending}
            >
              Discard
            </Button>
          )}
          <Button
            size="sm"
            onClick={() => save.mutate()}
            disabled={!canWrite || !dirty || !scheduleValid || !retentionValid || save.isPending}
            title={canWrite ? undefined : "Requires editor access on this project"}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>
    </section>
  );
}
