"use client";

import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { relativeTime } from "@/lib/format";
import { Save, Check, ShieldAlert, ShieldCheck } from "lucide-react";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { StatusPill } from "@/components/ui/status-pill";
import { RUN_STATUS, type StatusMeta } from "@/lib/status";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { cn } from "@/lib/utils";
import { useCan, Perms } from "@/features/auth";
import { ControlPlaneBackupSection } from "@/components/settings/ControlPlaneBackupSection";
import { addonBackupCoverageText, summarizeAddonBackups } from "@/components/health/backupCoverage";

interface HealthEntry {
  healthy: boolean;
  schedule?: string;
  lastSuccessAt?: string;
  detail: string;
}
interface AddonBackupRow {
  addon: string;
  schedule?: string;
  cronJobPresent?: boolean;
  secretPresent?: boolean;
  project?: string;
  namespace: string;
  kind?: string;
  instanceShared?: boolean;
  covered: boolean;
  healthy: boolean;
  unprotected?: boolean;
  lastSuccessAt?: string;
  lastScheduleAt?: string;
  detail?: string;
}
interface ServiceVolumeRow {
  service: string;
  project?: string;
  volumes: string[];
  detail?: string;
}
interface BackupHealthResp {
  backup: HealthEntry;
  registryGC: HealthEntry;
  addonBackups?: AddonBackupRow[];
  addonBackupsComplete?: boolean;
  serviceVolumes?: ServiceVolumeRow[];
}

type CoverageBadge = "on-schedule" | "overdue" | "unprotected" | "via-instance" | "not-covered";

function badgeFor(row: AddonBackupRow): CoverageBadge {
  if (row.healthy && row.unprotected) return "unprotected";
  if (!row.covered) return row.instanceShared ? "via-instance" : "not-covered";
  return row.healthy ? "on-schedule" : "overdue";
}

// Green means the last scheduled run landed; overdue means a scheduled
// run hasn't succeeded since (failed, or the job can't run). The two
// coverage states are facts about the setup, so they stay neutral, except
// a database with no dump yet, which is a data risk.
const BADGE: Record<CoverageBadge, StatusMeta> = {
  "on-schedule": { ...RUN_STATUS.succeeded, label: "On schedule" },
  overdue: { ...RUN_STATUS.failed, label: "Overdue" },
  unprotected: { label: "Unprotected", tone: "warning" },
  "via-instance": { label: "Via instance", tone: "neutral" },
  "not-covered": { label: "Not backed up", tone: "neutral" },
};

function CoverageRow({
  name,
  badge,
  age,
  detail,
}: {
  name: string;
  badge: CoverageBadge;
  age?: string;
  detail?: string;
}) {
  return (
    <li className="flex items-center gap-3 px-4 py-2 text-sm sm:px-5" title={detail}>
      <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-[var(--text-primary)]">{name}</span>
      {age && (
        <span className="shrink-0 text-xs text-[var(--text-tertiary)]" title="Last run that succeeded">
          {age}
        </span>
      )}
      <StatusPill status={BADGE[badge]} variant="tinted" />
    </li>
  );
}

function AddonBackupList({
  addons,
  volumes,
  complete,
}: {
  addons: AddonBackupRow[];
  volumes: ServiceVolumeRow[];
  complete: boolean;
}) {
  if (addons.length === 0 && volumes.length === 0) return null;
  const rank: Record<CoverageBadge, number> = { overdue: 0, unprotected: 1, "not-covered": 2, "on-schedule": 3, "via-instance": 4 };
  const sorted = [...addons].sort(
    (a, b) => rank[badgeFor(a)] - rank[badgeFor(b)] || a.addon.localeCompare(b.addon),
  );
  return (
    <SettingsSection
      id="addon-backups"
      title="Addon backups"
      description={
        <>
          Overdue first, then addons with no schedule. Ages are the last run that succeeded.
          {!complete && <span className="ml-1 text-[var(--warning)]">Partial list: some namespaces couldn&apos;t be read.</span>}
        </>
      }
      flush
    >
      <ul className="divide-y divide-[var(--border-subtle)]">
        {sorted.map((a) => (
          <CoverageRow
            key={`${a.namespace}/${a.addon}`}
            name={a.addon}
            badge={badgeFor(a)}
            age={a.lastSuccessAt ? relativeTime(a.lastSuccessAt) : undefined}
            detail={a.detail}
          />
        ))}
        {volumes.map((v) => (
          <CoverageRow
            key={`${v.project ?? ""}/${v.service}`}
            name={`${v.service} · ${v.volumes.join(", ")}`}
            badge="not-covered"
            detail={v.detail}
          />
        ))}
      </ul>
    </SettingsSection>
  );
}

function HealthRow({ title, h }: { title: string; h: HealthEntry }) {
  const Icon = h.healthy ? ShieldCheck : ShieldAlert;
  return (
    <div className="flex items-start gap-3">
      <Icon
        className={cn("mt-0.5 h-4 w-4 shrink-0", h.healthy ? "text-[var(--success)]" : "text-[var(--warning)]")}
      />
      <div className="min-w-0">
        <p className="text-sm font-medium text-[var(--text-primary)]">{title}</p>
        <p className={cn("mt-0.5 text-sm", h.healthy ? "text-[var(--text-secondary)]" : "text-[var(--warning)]")}>
          {h.detail}
        </p>
        {h.lastSuccessAt && (
          <p className="mt-0.5 text-xs text-[var(--text-tertiary)]">
            Last success {new Date(h.lastSuccessAt).toLocaleString()}
            {h.schedule && (
              <>
                {" · schedule "}
                <span className="font-mono">{h.schedule}</span>
              </>
            )}
          </p>
        )}
      </div>
    </div>
  );
}

// MaintenanceHealthBanners surfaces the health of the two silent
// cluster-maintenance jobs: the control-plane DB backup (opt-in, self-
// suspends silently) and the registry GC (whose failure fills the
// registry PVC until builds break). Separate from the addon-backup S3
// config form below.
// CoverageHeadline leads the page: how many addons have no schedule and
// how many scheduled ones are overdue. Green only when every addon is
// covered, nothing is overdue and S3 is set up.
function CoverageHeadline({ rows, complete, s3 }: { rows: AddonBackupRow[]; complete: boolean; s3: "yes" | "no" | "unknown" }) {
  const c = summarizeAddonBackups(rows);
  const good = c.uncovered === 0 && c.failing === 0 && s3 === "yes" && complete;
  const parts = [c.total === 0 ? "No addons to back up" : addonBackupCoverageText(c)];
  if (c.failing > 0) parts.push(`${c.failing} overdue`);
  const Icon = good ? ShieldCheck : ShieldAlert;
  return (
    <div
      role="status"
      className={cn(
        "flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border px-4 py-3",
        good ? "border-[var(--success)]/35 bg-[var(--success-subtle)]" : "border-[var(--warning)]/35 bg-[var(--warning-subtle)]",
      )}
    >
      <Icon aria-hidden className={cn("h-5 w-5 shrink-0", good ? "text-[var(--success)]" : "text-[var(--warning)]")} />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-semibold text-[var(--text-primary)]">{parts.join(" · ")}</p>
        <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
          {s3 === "no"
            ? "S3 storage isn't set up, so no scheduled backup can land anywhere."
            : good
              ? "Every addon's last scheduled backup landed."
              : c.uncovered > 0
                ? "Addons without a schedule aren't backed up. Set one on each addon's Backups tab."
                : "A scheduled run hasn't succeeded since its last tick. Open the addon's Backups tab for the log."}
          {!complete && " Some namespaces couldn't be read, so the counts may be low."}
        </p>
      </div>
      {s3 === "no" && (
        <Button size="sm" render={<a href="#s3-storage" />} nativeButton={false}>
          Set up S3
        </Button>
      )}
    </div>
  );
}

function MaintenanceHealthBanners({ s3 }: { s3: "yes" | "no" | "unknown" }) {
  const health = useQuery({
    queryKey: ["admin", "backup-health"],
    queryFn: () => api<BackupHealthResp>("/api/admin/backup-health"),
    refetchInterval: 60_000,
  });
  if (health.isPending) return null;
  // An errored fetch must NOT silently render nothing — that reads as
  // "backups are healthy" when we actually don't know. Surface the
  // failure so an operator investigates instead of assuming green.
  if (health.isError || !health.data) {
    return (
      <SettingsNotice tone="warning" title="Couldn't load backup and registry-GC health">
        {health.error instanceof Error ? `${health.error.message}. ` : ""}Backup status is unknown; check the
        control plane.
      </SettingsNotice>
    );
  }
  const { backup, registryGC } = health.data;
  return (
    <>
      <CoverageHeadline rows={health.data.addonBackups ?? []} complete={health.data.addonBackupsComplete !== false} s3={s3} />
      <SettingsSection title="Maintenance jobs" description="Silent cluster jobs that break things when they stop running.">
        <HealthRow title="Control-plane database backup" h={backup} />
        <HealthRow title="Registry garbage collection" h={registryGC} />
      </SettingsSection>
      <AddonBackupList
        addons={health.data.addonBackups ?? []}
        volumes={health.data.serviceVolumes ?? []}
        complete={health.data.addonBackupsComplete !== false}
      />
    </>
  );
}

interface BackupSettings {
  bucket: string;
  endpoint: string;
  region: string;
  accessKeyId: string;
  secretAccessKey?: string;
  hasSecret: boolean;
}

// Backups settings page. Writes to /api/admin/backup-settings which
// upserts the kuso-backup-s3 Secret. Once configured, every addon
// with a backup.schedule kicks pg_dump → S3 every cron tick.
export default function BackupSettingsPage() {
  const qc = useQueryClient();
  const isAdmin = useCan(Perms.SettingsAdmin);
  const settings = useQuery({
    queryKey: ["admin", "backup-settings"],
    queryFn: () => api<BackupSettings>("/api/admin/backup-settings"),
  });
  const [form, setForm] = useState<BackupSettings>({
    bucket: "",
    endpoint: "",
    region: "auto",
    accessKeyId: "",
    secretAccessKey: "",
    hasSecret: false,
  });
  useEffect(() => {
    if (settings.data) {
      setForm({
        ...settings.data,
        secretAccessKey: "", // never preload — empty = leave alone on save
      });
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: (body: BackupSettings) =>
      api("/api/admin/backup-settings", { method: "PUT", body }),
    onSuccess: () => {
      toast.success("Backup settings saved");
      qc.invalidateQueries({ queryKey: ["admin", "backup-settings"] });
    },
    onError: (e) => toastError(e, "Save failed"),
  });

  return (
    <SettingsPage
      title="Backups"
      description="Backup health across the cluster, the control-plane database, and the S3 bucket every addon backup uploads to."
    >
      <MaintenanceHealthBanners s3={settings.data ? (settings.data.hasSecret ? "yes" : "no") : "unknown"} />

      {isAdmin && <ControlPlaneBackupSection />}

      {settings.isPending ? (
        <Skeleton className="h-72 w-full rounded-md" />
      ) : settings.isError ? (
        <QueryErrorState
          what="backup settings"
          error={settings.error}
          onRetry={() => void settings.refetch()}
        />
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!form.bucket || !form.endpoint || !form.accessKeyId) {
              toast.error("bucket, endpoint, accessKeyId are required");
              return;
            }
            if (!form.hasSecret && !form.secretAccessKey) {
              toast.error("secret access key required on first save");
              return;
            }
            save.mutate(form);
          }}
        >
          <SettingsSection
            id="s3-storage"
            title="S3 storage"
            description="Credentials every addon backup job uses. Schedule backups per addon on its Backups tab (open the addon on the project canvas)."
            footerHint={
              settings.data?.hasSecret ? (
                <span className="inline-flex items-center gap-1">
                  <Check className="h-3.5 w-3.5 text-[var(--success)]" /> Configured
                </span>
              ) : (
                "Not configured"
              )
            }
            footer={
              <Button size="sm" type="submit" disabled={save.isPending}>
                <Save className="h-3.5 w-3.5" />
                {save.isPending ? "Saving…" : "Save"}
              </Button>
            }
          >
            <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
              <Field
                label="Bucket"
                hint="S3 bucket name."
                value={form.bucket}
                onChange={(v) => setForm((s) => ({ ...s, bucket: v }))}
              />
              <Field
                label="Region"
                hint="Leave “auto” for most providers."
                value={form.region}
                onChange={(v) => setForm((s) => ({ ...s, region: v }))}
              />
            </div>
            <Field
              label="Endpoint"
              hint="An https:// URL for any S3-compatible store."
              placeholder="e.g. https://s3.fr-par.scw.cloud"
              value={form.endpoint}
              onChange={(v) => setForm((s) => ({ ...s, endpoint: v }))}
            />
            <div className="grid grid-cols-1 gap-5 sm:grid-cols-2">
              <Field
                label="Access key ID"
                value={form.accessKeyId}
                onChange={(v) => setForm((s) => ({ ...s, accessKeyId: v }))}
              />
              <Field
                label="Secret access key"
                hint={form.hasSecret ? "Leave empty to keep the current key." : "Required."}
                type="password"
                value={form.secretAccessKey ?? ""}
                onChange={(v) => setForm((s) => ({ ...s, secretAccessKey: v }))}
              />
            </div>
          </SettingsSection>
        </form>
      )}
    </SettingsPage>
  );
}

function Field({
  label,
  hint,
  value,
  onChange,
  type = "text",
  placeholder,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (v: string) => void;
  type?: "text" | "password";
  placeholder?: string;
}) {
  const id = `backup-${label.toLowerCase().replace(/\s+/g, "-")}`;
  return (
    <SettingsField label={label} hint={hint} htmlFor={id}>
      <Input
        id={id}
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="font-mono"
        autoComplete="off"
        spellCheck={false}
      />
    </SettingsField>
  );
}
