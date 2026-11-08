"use client";

import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { toast } from "sonner";
import { relativeTime } from "@/lib/format";
import { Save, Check, ShieldAlert, ShieldCheck } from "lucide-react";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { StatusPill } from "@/components/ui/status-pill";
import { RUN_STATUS, type StatusMeta } from "@/lib/status";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { cn } from "@/lib/utils";
import { useCan, Perms } from "@/features/auth";
import { ControlPlaneBackupSection } from "@/components/settings/ControlPlaneBackupSection";

interface HealthEntry {
  healthy: boolean;
  schedule?: string;
  lastSuccessAt?: string;
  detail: string;
}
interface AddonBackupRow {
  addon: string;
  project?: string;
  namespace: string;
  kind?: string;
  instanceShared?: boolean;
  covered: boolean;
  healthy: boolean;
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

type CoverageBadge = "succeeded" | "failed" | "via-instance" | "not-covered";

function badgeFor(row: AddonBackupRow): CoverageBadge {
  if (!row.covered) return row.instanceShared ? "via-instance" : "not-covered";
  return row.healthy ? "succeeded" : "failed";
}

// Run outcomes use the shared vocabulary; the two coverage states are
// facts about the setup, not runs, so they stay neutral.
const BADGE: Record<CoverageBadge, StatusMeta> = {
  succeeded: RUN_STATUS.succeeded,
  failed: RUN_STATUS.failed,
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
  const rank: Record<CoverageBadge, number> = { failed: 0, succeeded: 1, "via-instance": 2, "not-covered": 3 };
  const sorted = [...addons].sort(
    (a, b) => rank[badgeFor(a)] - rank[badgeFor(b)] || a.addon.localeCompare(b.addon),
  );
  return (
    <SettingsSection
      title="Addon backups"
      description={
        <>
          Failures first. Ages are the last run that succeeded.
          {!complete && <span className="ml-1 text-[var(--warning)]">Partial list: some namespaces couldn&apos;t be read.</span>}
        </>
      }
      flush
    >
      <ul className="max-h-[480px] divide-y divide-[var(--border-subtle)] overflow-y-auto">
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
function MaintenanceHealthBanners() {
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
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  return (
    <SettingsPage
      title="Backups"
      description="Backup health across the cluster, the control-plane database, and the S3 bucket every addon backup uploads to."
    >
      <MaintenanceHealthBanners />

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
