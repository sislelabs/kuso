"use client";

import { useEffect, useState } from "react";
import { motion, AnimatePresence } from "motion/react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { RotateCcw } from "lucide-react";
import {
  useAddons,
  listBackups,
  restoreBackup,
  type BackupObject,
} from "@/features/projects";
import { Button } from "@/components/ui/button";
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
  // Pull every postgres addon in the project so the user can pick a
  // non-destructive restore target. Postgres-only because the
  // existing restore Job assumes pg_dump format.
  const allAddons = useAddons(project);
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
      <div className="m-5 rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
        {noS3 ? "Backups not set up yet" : `Backups unavailable: ${msg}`}
        <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
          {noS3 ? (
            <>
              Backups need S3 (or compatible) credentials. Add them in{" "}
              <a href="/settings/backups" className="text-[var(--accent)] underline">
                /settings/backups
              </a>
              , then put <span className="font-mono">backup.schedule</span> on this
              addon in <span className="font-mono">kuso.yml</span> to start the
              CronJob.
            </>
          ) : (
            <>
              Detail:{" "}
              <span className="font-mono text-[var(--text-secondary)]">{msg}</span>
            </>
          )}
        </p>
      </div>
    );
  }

  const items = list.data ?? [];
  if (items.length === 0) {
    return (
      <p className="m-5 rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
        No backups yet. The CronJob will drop one once its schedule fires.
      </p>
    );
  }
  // Newest first.
  const sorted = [...items].sort((a, b) => (b.when ?? "").localeCompare(a.when ?? ""));

  return (
    <div className="p-5">
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
