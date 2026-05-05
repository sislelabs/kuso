"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import {
  useAddons,
  listBackups,
  restoreBackup,
  updateAddon,
  type BackupObject,
} from "@/features/projects";
import type { KusoAddon } from "@/types/projects";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
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
  const restore = useMutation({
    mutationFn: ({ key, into }: { key: string; into?: string }) =>
      restoreBackup(project, addon, key, into),
    onSuccess: (res) => {
      toast.success(`Restore job started: ${res.job}`);
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
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} />
        <div className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          {noS3 ? "Backups not set up yet" : `Backups unavailable: ${msg}`}
          <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
            {noS3 ? (
              <>
                Add S3 (or compatible) credentials in{" "}
                <a href="/settings/backups" className="text-[var(--accent)] underline">
                  /settings/backups
                </a>
                , then set a schedule above to start the CronJob.
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
        <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} />
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
      <BackupScheduleEditor project={project} addon={addon} thisAddon={thisAddon} />
      <header className="mb-3 flex items-center justify-between">
        <h3 className="font-heading text-sm font-semibold tracking-tight">Backups</h3>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {items.length} {items.length === 1 ? "object" : "objects"} · auto-refresh 30s
        </span>
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
              disabled={restore.isPending}
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
        onConfirm={(key, into) => {
          restore.mutate({ key, into });
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
  onConfirm: (key: string, into?: string) => void;
}) {
  const [target, setTarget] = useState<string>("");
  // Reset target to in-place every time a new backup gets picked so
  // the user explicitly opts into the cross-addon path each time.
  useEffect(() => {
    if (item) setTarget("");
  }, [item]);
  return (
    <AnimatePresence>
      {item && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.1 }}
          className="fixed inset-0 z-[60] flex items-center justify-center bg-[rgba(8,8,11,0.7)] p-4"
          onClick={onCancel}
        >
          <motion.div
            initial={{ scale: 0.96, y: 4 }}
            animate={{ scale: 1, y: 0 }}
            exit={{ scale: 0.96, y: 4 }}
            transition={{ duration: 0.12 }}
            onClick={(e) => e.stopPropagation()}
            className={cn(
              "w-full max-w-md rounded-md border bg-[var(--bg-elevated)] p-5",
              target === "" ? "border-red-500/40" : "border-amber-500/40",
            )}
          >
            <h3 className="text-base font-semibold">Restore this backup?</h3>
            <p className="mt-2 text-xs text-[var(--text-secondary)]">
              Pipes <span className="font-mono">{tail(item.key)}</span> into the chosen
              target database via <span className="font-mono">psql</span>.
            </p>
            <div className="mt-4 space-y-2">
              <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                Restore into
              </label>
              <select
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                className="block w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1.5 font-mono text-[12px]"
              >
                <option value="">{sourceAddon} (overwrite — destructive)</option>
                {siblings.map((s) => (
                  <option key={s} value={s}>
                    {s} (non-destructive — leaves {sourceAddon} alone)
                  </option>
                ))}
              </select>
              {target === "" ? (
                <p className="font-mono text-[10px] text-red-400">
                  Existing tables in {sourceAddon} will be overwritten. Cannot be
                  undone.
                </p>
              ) : (
                <p className="font-mono text-[10px] text-amber-400">
                  {sourceAddon} stays as-is; the dump goes into {target}. {target} must
                  already exist + be a postgres addon.
                </p>
              )}
            </div>
            <div className="mt-4 flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onCancel} disabled={pending}>
                Cancel
              </Button>
              <Button
                variant={target === "" ? "destructive" : "default"}
                size="sm"
                onClick={() => onConfirm(item.key, target || undefined)}
                disabled={pending}
              >
                <RotateCcw className="h-3 w-3" />
                {pending ? "Starting…" : `Restore into ${target || sourceAddon}`}
              </Button>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

// Common cron presets surfaced as quick-pick buttons. The user can
// still type any 5-field cron expression in the input; presets are a
// keyboard-saver for the 95% case.
const SCHEDULE_PRESETS: { label: string; cron: string }[] = [
  { label: "Hourly", cron: "0 * * * *" },
  { label: "Every 6h", cron: "0 */6 * * *" },
  { label: "Daily 03:00", cron: "0 3 * * *" },
  { label: "Weekly (Sun 03:00)", cron: "0 3 * * 0" },
];

// 5-field cron regex mirrors the server's addons.cronExpr5. Pre-flight
// check so a typo turns the Save button into a visible warning rather
// than a 400 toast after the round-trip.
const CRON_RE = /^[\d\*\/,\-?]+\s+[\d\*\/,\-?]+\s+[\d\*\/,\-?]+\s+[\d\*\/,\-?]+\s+[\d\*\/,\-?]+$/;

// BackupScheduleEditor lets the user enable / change / disable the
// per-addon backup CronJob. Schedule = cron expression; retentionDays
// = N days after which the cronjob's prune step deletes old objects
// (0 = keep forever). PATCHes the addon CR via /api/.../addons/{a}.
function BackupScheduleEditor({
  project,
  addon,
  thisAddon,
}: {
  project: string;
  addon: string;
  thisAddon?: KusoAddon;
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
        <div className="space-y-1">
          <label className="block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            Cron
          </label>
          <Input
            value={schedule}
            onChange={(e) => setSchedule(e.target.value)}
            placeholder="leave empty to disable · e.g. 0 3 * * *"
            spellCheck={false}
            className={cn(
              "h-8 font-mono text-[12px]",
              !scheduleValid && "border-red-500/60",
            )}
          />
          {!scheduleValid && (
            <p className="font-mono text-[10px] text-red-400">
              Must be a 5-field cron expression (or empty to disable).
            </p>
          )}
          <div className="flex flex-wrap gap-1.5 pt-1">
            {SCHEDULE_PRESETS.map((p) => (
              <button
                key={p.cron}
                type="button"
                onClick={() => setSchedule(p.cron)}
                className="rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-0.5 font-mono text-[10px] text-[var(--text-secondary)] hover:border-[var(--accent)]/50 hover:text-[var(--accent)]"
              >
                {p.label}
              </button>
            ))}
          </div>
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
            disabled={!dirty || !scheduleValid || !retentionValid || save.isPending}
          >
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </div>
      </div>
    </section>
  );
}
