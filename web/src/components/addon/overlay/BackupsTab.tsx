"use client";

import { Select } from "@/components/ui/select";
import { useEffect, useState } from "react";
import { useQueryClient, useMutation } from "@tanstack/react-query";
import { RotateCcw, Download } from "lucide-react";
import { Callout } from "@/components/ui/callout";
import {
  downloadStoredBackup,
  useAddonBackups,
  addonBackupsQueryKey,
  backupSummary,
  backupsListState,
  isBackupsNotConfigured,
} from "@/features/addons";
import {
  useAddons,
  restoreBackup,
  downloadAddonBackup,
  updateAddon,
  type BackupObject,
} from "@/features/projects";
import type { KusoAddon } from "@/types/projects";
import { Button } from "@/components/ui/button";
import { SaveButton } from "@/components/shared/SaveButton";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { CronPicker } from "@/components/shared/CronPicker";
import { useCanOnProject, Perms } from "@/features/auth";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
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
      title="Dump the database right now and save it to this computer. Doesn't need S3."
      onClick={async () => {
        setBusy(true);
        try {
          await downloadAddonBackup(project, addon);
        } catch (e) {
          toastError(e, "Download failed");
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

// BackupsHeader's meta line is the same backupSummary sentence the
// Overview tile shows. "Back up now" lives only in the overlay header.
function BackupsHeader({
  project,
  addon,
  kind,
  meta,
}: {
  project: string;
  addon: string;
  kind?: string;
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
  const list = useAddonBackups(project, addon);
  const summary = backupSummary(thisAddon, backupsListState(list));
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
      qc.invalidateQueries({ queryKey: addonBackupsQueryKey(project, addon) });
    },
    onError: (e) => toastError(e, "Restore failed"),
  });
  const [confirmKey, setConfirmKey] = useState<string | null>(null);
  const hasSchedule = !!thisAddon?.spec.backup?.schedule;
  const sharedInstance = thisAddon?.spec.useInstanceAddon;
  // A database on the shared instance postgres is dumped by that addon's
  // job; the chart renders no CronJob for it, so a schedule here would
  // save and silently never run.
  const schedule = sharedInstance ? (
    <Callout tone="info" title={`Backed up with ${sharedInstance}`}>
      This database lives on the shared instance database <span className="font-mono text-xs">{sharedInstance}</span>,
      which dumps it on its own schedule. Those backups are listed below.
    </Callout>
  ) : (
    <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} canWrite={canWrite} />
  );
  const canDownload = useCanOnProject(project, Perms.SecretsRead);
  const [downloading, setDownloading] = useState<string | null>(null);
  const download = async (key: string) => {
    setDownloading(key);
    try {
      await downloadStoredBackup(project, addon, key);
    } catch (e) {
      toastError(e, "Download failed");
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
    // Only the "not configured" 503 is the setup hint; any other error
    // (bad credentials, unreachable endpoint) is a real outage.
    const noS3 = isBackupsNotConfigured(list.error);
    return (
      <div className="space-y-4 p-5">
        {schedule}
        <BackupsHeader project={project} addon={addon} kind={thisAddon?.spec.kind} meta={summary.sentence} />
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
        {schedule}
        <BackupsHeader project={project} addon={addon} kind={thisAddon?.spec.kind} meta={summary.sentence} />
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          {hasSchedule || sharedInstance
            ? "No backups yet. The first one lands when the schedule fires, or use Back up now at the top."
            : "No backups yet. Turn on a schedule above to back up to S3 automatically."}
        </p>
      </div>
    );
  }
  // Newest first.
  const sorted = [...items].sort((a, b) => (b.when ?? "").localeCompare(a.when ?? ""));

  return (
    <div className="space-y-4 p-5">
      {schedule}
      <BackupsHeader
        project={project}
        addon={addon}
        kind={thisAddon?.spec.kind}
        meta={`${summary.sentence} · ${items.length} in S3, newest first`}
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
  // An emptied field parses as 0 ("keep forever"); make it a typo instead.
  const retentionValid =
    retention.trim() !== "" && Number.isInteger(retentionNum) && retentionNum >= 0 && retentionNum <= 3650;

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
      qc.invalidateQueries({ queryKey: addonBackupsQueryKey(project, addon) });
    },
    onError: (e) => toastError(e, "Save failed"),
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
          <SaveButton
            dirty={dirty}
            saving={save.isPending}
            onClick={() => save.mutate()}
            blockedReason={
              !canWrite
                ? "Requires editor access on this project"
                : !scheduleValid
                  ? "Fix the schedule first"
                  : !retentionValid
                    ? "Fix the retention first"
                    : undefined
            }
          />
        </div>
      </div>
    </section>
  );
}
