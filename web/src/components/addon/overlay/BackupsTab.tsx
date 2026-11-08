"use client";

import { Select } from "@/components/ui/select";
import { useEffect, useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { RotateCcw, Download, DatabaseBackup } from "lucide-react";
import { Callout } from "@/components/ui/callout";
import { runBackupNow, downloadStoredBackup } from "@/features/addons";
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
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { CronPicker } from "@/components/shared/CronPicker";
import { useCanOnProject, Perms } from "@/features/auth";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";
import { ApiError } from "@/lib/api-client";

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
      title="Dump the database right now and save it to this computer. Doesn't need S3."
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
      {busy ? "Preparing…" : "Download fresh dump"}
    </Button>
  );
}

// BackupNowButton runs the scheduled backup job once, on demand: same
// dump, S3 upload and prune as the CronJob. Needs a schedule, since the
// job only exists while one is set.
function BackupNowButton({
  project,
  addon,
  hasSchedule,
  canWrite,
}: {
  project: string;
  addon: string;
  hasSchedule: boolean;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const run = useMutation({
    mutationFn: () => runBackupNow(project, addon),
    onSuccess: (res) => {
      toast.success("Backup started", { description: `Job ${res.job}. It appears below once the upload finishes.` });
      void qc.invalidateQueries({ queryKey: ["addons", project, addon, "backups"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Couldn't start the backup"),
  });
  return (
    <Button
      size="sm"
      onClick={() => run.mutate()}
      disabled={!canWrite || !hasSchedule || run.isPending}
      title={
        !canWrite
          ? "Requires editor access on this project"
          : hasSchedule
            ? "Back up to S3 now"
            : "Turn on scheduled backups above first"
      }
    >
      <DatabaseBackup className="h-3 w-3" />
      {run.isPending ? "Starting…" : "Back up now"}
    </Button>
  );
}

function BackupsHeader({
  project,
  addon,
  kind,
  hasSchedule,
  canWrite,
  meta,
}: {
  project: string;
  addon: string;
  kind?: string;
  hasSchedule: boolean;
  canWrite: boolean;
  meta?: React.ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-center justify-between gap-3">
      <div className="min-w-0">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
        {meta && <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">{meta}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <DownloadBackupButton project={project} addon={addon} kind={kind} />
        {kind === "postgres" && (
          <BackupNowButton project={project} addon={addon} hasSchedule={hasSchedule} canWrite={canWrite} />
        )}
      </div>
    </header>
  );
}

// The list endpoint caps at 200 objects (handlers/backups.go); hitting
// the cap means older ones were cut off.
const BACKUP_LIST_CAP = 200;

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
  // There's no restore-progress endpoint yet, so at least keep a visible
  // record of what was started instead of a toast that vanishes.
  const [lastRestore, setLastRestore] = useState<{ job: string; key: string; into?: string; at: Date } | null>(null);
  const restore = useMutation({
    mutationFn: ({ key, into, confirm }: { key: string; into?: string; confirm?: string }) =>
      // In-place restore (no `into`) is destructive; the server requires
      // `confirm` to echo the addon name. We forward exactly what the
      // user typed in the dialog — auto-supplying it here would defeat
      // the server's type-the-name safeguard.
      restoreBackup(project, addon, key, into, confirm),
    onSuccess: (res, vars) => {
      toast.success(`Restore job ${res.job} started`);
      setLastRestore({ job: res.job, key: vars.key, into: vars.into, at: new Date() });
      setConfirmKey(null);
      qc.invalidateQueries({ queryKey: ["addons", project, addon, "backups"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Restore failed"),
  });
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  const hasSchedule = !!thisAddon?.spec.backup?.schedule;
  const canDownload = useCanOnProject(project, Perms.SecretsRead);
  const [downloading, setDownloading] = useState<string | null>(null);
  const download = async (key: string) => {
    setDownloading(key);
    try {
      await downloadStoredBackup(project, addon, key);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Download failed");
    } finally {
      setDownloading(null);
    }
  };
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
    // 503 + "not configured" is the server's "S3 not set up" signal. A
    // 503 for anything else (bad credentials, unreachable endpoint) is a
    // real outage and must show its message, not the setup hint.
    const noS3 =
      list.error instanceof ApiError && list.error.status === 503 && /not configured/i.test(msg);
    return (
      <div className="space-y-4 p-5">
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
        <BackupsHeader project={project} addon={addon} kind={thisAddon?.spec.kind} hasSchedule={hasSchedule} canWrite={canWrite} />
        {noS3 ? (
          <Callout tone="info" title="Off-site backups aren't set up yet">
            Add S3 credentials in{" "}
            <a href="/settings/backups" className="text-[var(--link)] underline underline-offset-2">
              Settings → Backups
            </a>{" "}
            (admin), then turn on a schedule above. You can still download a fresh dump any time.
          </Callout>
        ) : (
          <Callout tone="warning" title="Couldn't list backups">
            <span className="font-mono text-xs">{msg}</span>
          </Callout>
        )}
      </div>
    );
  }

  const items = list.data ?? [];
  if (items.length === 0) {
    return (
      <div className="space-y-4 p-5">
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
        <BackupsHeader project={project} addon={addon} kind={thisAddon?.spec.kind} hasSchedule={hasSchedule} canWrite={canWrite} />
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          {hasSchedule
            ? "No backups yet. The first one lands when the schedule fires, or press Back up now."
            : "No backups yet. Turn on a schedule above to back up to S3 automatically."}
        </p>
      </div>
    );
  }
  // Newest first.
  const sorted = [...items].sort((a, b) => (b.when ?? "").localeCompare(a.when ?? ""));

  return (
    <div className="space-y-4 p-5">
      <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
      <BackupsHeader
        project={project}
        addon={addon}
        kind={thisAddon?.spec.kind}
        hasSchedule={hasSchedule}
        canWrite={canWrite}
        meta={`${items.length} ${items.length === 1 ? "backup" : "backups"} in S3, newest first`}
      />
      {lastRestore && (
        <Callout tone="info" role="status" title={`Restore started ${relativeTime(lastRestore.at.toISOString())}`}>
          Job <span className="font-mono text-xs">{lastRestore.job}</span> is loading{" "}
          <span className="font-mono text-xs">{tail(lastRestore.key)}</span>
          {lastRestore.into ? ` into ${lastRestore.into}` : ""}. Progress isn&apos;t tracked here yet; check the
          job&apos;s logs before relying on the data.
        </Callout>
      )}
      {items.length >= BACKUP_LIST_CAP && (
        <p className="text-xs text-[var(--text-tertiary)]">
          Showing the newest {BACKUP_LIST_CAP} objects; older backups are in the bucket but not listed.
        </p>
      )}
      <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        {sorted.map((b) => (
          <li
            key={b.key}
            className="flex items-center gap-3 border-b border-[var(--border-subtle)] px-3 py-2 last:border-b-0"
          >
            <div className="min-w-0 flex-1">
              <div className="text-sm text-[var(--text-primary)]" title={b.when}>
                {b.when ? relativeTime(b.when) : "Unknown time"}
                <span className="ml-2 text-xs text-[var(--text-tertiary)]">{formatBytes(b.size)}</span>
              </div>
              <div className="mt-0.5 truncate font-mono text-xs text-[var(--text-tertiary)]" title={b.key}>
                {tail(b.key)}
              </div>
            </div>
            {canDownload && (
              <Button
                size="sm"
                variant="ghost"
                disabled={downloading !== null}
                title="Save this backup to your computer (recorded in the audit log)"
                onClick={() => void download(b.key)}
              >
                <Download className="h-3 w-3" />
                {downloading === b.key ? "Downloading…" : "Download"}
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={restore.isPending || !canWrite}
              title={canWrite ? "Overwrite the database with this backup" : "Requires editor access on this project"}
              onClick={() => setConfirmKey(b.key)}
              className="border-[var(--error)]/40 text-[var(--error)] hover:border-[var(--error)]/70 hover:bg-[var(--error-subtle)] hover:text-[var(--error)]"
            >
              <RotateCcw className="h-3 w-3" />
              Restore…
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
          // The dialog stays open (pending) until the server accepts the
          // job, so a rejected restore shows its error in context.
          restore.mutate({ key, into, confirm });
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
            <Select
              size="sm"
              id="restore-target"
              aria-label="Restore into"
              value={target}
              onChange={(e) => setTarget(e.target.value)}
              className="font-mono"
            >
              <option value="">{sourceAddon} (overwrite)</option>
              {siblings.map((s) => (
                <option key={s} value={s}>
                  {s} (overwrite)
                </option>
              ))}
            </Select>
            <p className="text-[var(--error)]">Existing tables in {target || sourceAddon} are overwritten.</p>
            {!inPlace && (
              <p className="text-[var(--text-tertiary)]">
                {sourceAddon} is left untouched. {target} must be an existing postgres addon.
              </p>
            )}
          </div>
        )
      }
      confirmLabel={`Restore into ${target || sourceAddon}`}
      // Both paths drop and recreate the target's tables (the dumps are
      // --clean), so both need the target typed; the server enforces it too.
      typeToConfirm={target || sourceAddon}
      destructive
      pending={pending}
      onConfirm={() => {
        if (item) onConfirm(item.key, target || undefined, target || sourceAddon);
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
        <Badge variant={enabled ? "success" : "neutral"}>{enabled ? "On" : "Off"}</Badge>
      </header>

      <div className="space-y-3">
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-sm">
            <Switch
              on={trimmed !== ""}
              labelledBy="backup-schedule-label"
              disabled={!canWrite}
              onChange={(on) => {
                if (on) {
                  // Re-enabling: hand back the user's last good
                  // schedule, or fall back to a sensible daily-3am
                  // default if they're enabling for the first time.
                  setSchedule(initialSchedule || "0 3 * * *");
                } else {
                  setSchedule("");
                }
              }}
            />
            <span id="backup-schedule-label">Back up to S3 on a schedule</span>
          </div>
          {trimmed !== "" && (
            <CronPicker value={schedule} onChange={setSchedule} />
          )}
          {!scheduleValid && (
            <p className="text-xs text-[var(--error)]">
              Custom cron must be 5 fields (e.g. <code>0 3 * * *</code>).
            </p>
          )}
        </div>

        {enabled && (
        <div className="space-y-1">
          <label htmlFor="backup-retention" className="block text-xs font-medium text-[var(--text-secondary)]">
            Keep backups for (days)
          </label>
          <Input
            id="backup-retention"
            type="number"
            min={0}
            max={3650}
            value={retention}
            onChange={(e) => setRetention(e.target.value)}
            className={cn(
              "h-8 w-24 font-mono text-xs",
              !retentionValid && "border-[var(--error)]/60",
            )}
          />
          <p className="text-xs text-[var(--text-tertiary)]">
            {retentionNum === 0
              ? "0 keeps every backup forever."
              : `Each run deletes backups older than ${retentionNum} day${retentionNum === 1 ? "" : "s"} from S3. 0 keeps them forever.`}
          </p>
        </div>
        )}

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
