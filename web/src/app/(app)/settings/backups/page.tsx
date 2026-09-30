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
import { Save, HardDrive, Check, ShieldAlert, ShieldCheck } from "lucide-react";

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

type CoverageBadge = "ok" | "failing" | "via instance" | "not covered";

function badgeFor(row: AddonBackupRow): CoverageBadge {
  if (!row.covered) return row.instanceShared ? "via instance" : "not covered";
  return row.healthy ? "ok" : "failing";
}

const BADGE_TONE: Record<CoverageBadge, string> = {
  ok: "bg-[var(--success-subtle)] text-[var(--success)]",
  failing: "bg-[var(--error-subtle)] text-[var(--error)]",
  "via instance": "bg-[var(--bg-tertiary)] text-[var(--text-secondary)]",
  "not covered": "bg-[var(--bg-tertiary)] text-[var(--text-tertiary)]",
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
    <li className="flex items-center gap-2 px-3 py-1.5 text-[12px]" title={detail}>
      <span className="min-w-0 flex-1 truncate font-mono">{name}</span>
      {age && (
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]" title="last run exited OK">
          {age}
        </span>
      )}
      <span className={`rounded px-1.5 py-0.5 font-mono text-[10px] ${BADGE_TONE[badge]}`}>{badge}</span>
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
  const rank: Record<CoverageBadge, number> = { failing: 0, ok: 1, "via instance": 2, "not covered": 3 };
  const sorted = [...addons].sort(
    (a, b) => rank[badgeFor(a)] - rank[badgeFor(b)] || a.addon.localeCompare(b.addon),
  );
  return (
    <div className="mb-6 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <h2 className="border-b border-[var(--border-subtle)] px-3 py-2 text-[12px] font-semibold">
        Addon backups
        {!complete && (
          <span className="ml-2 font-mono text-[10px] font-normal text-[var(--warning)]">partial list</span>
        )}
      </h2>
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
            badge="not covered"
            detail={v.detail}
          />
        ))}
      </ul>
    </div>
  );
}

function HealthBanner({ title, h }: { title: string; h: HealthEntry }) {
  const tone = h.healthy
    ? "border-green-500/30 bg-green-500/5 text-[var(--success)]"
    : "border-[var(--warning)]/40 bg-[var(--warning-subtle)] text-[var(--warning)]";
  const Icon = h.healthy ? ShieldCheck : ShieldAlert;
  return (
    <div className={`flex items-start gap-2.5 rounded-md border px-3 py-2.5 text-[12px] leading-relaxed ${tone}`}>
      <Icon className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0">
        <p className="font-semibold">{title}</p>
        <p className="mt-0.5">{h.detail}</p>
        {h.lastSuccessAt && (
          <p className="mt-0.5 font-mono text-[10px] opacity-80">
            last success {new Date(h.lastSuccessAt).toLocaleString()}
            {h.schedule ? ` · schedule ${h.schedule}` : ""}
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
      <div className="mb-6 rounded-md border border-[var(--warning)]/30 bg-[var(--warning-subtle)] px-3 py-2 text-[12px] text-[var(--warning)]">
        Couldn&apos;t load backup / registry-GC health
        {health.error instanceof Error ? `: ${health.error.message}` : ""}. Backup
        status is unknown — check the control plane.
      </div>
    );
  }
  const { backup, registryGC } = health.data;
  return (
    <>
      <div className="mb-6 space-y-2.5">
        <HealthBanner title="Control-plane database backup" h={backup} />
        <HealthBanner title="Registry garbage-collection" h={registryGC} />
      </div>
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
    <div className="mx-auto max-w-2xl p-6 lg:p-8">
      <header className="mb-6 flex items-center gap-3">
        <HardDrive className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Backups</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            S3 credentials for addon backups. Every addon with a backup schedule uploads its
            dumps to this bucket.
          </p>
        </div>
      </header>

      <MaintenanceHealthBanners />

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
          className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]"
        >
          <Field
            label="bucket"
            hint="S3 bucket name"
            value={form.bucket}
            onChange={(v) => setForm((s) => ({ ...s, bucket: v }))}
          />
          <Field
            label="endpoint"
            hint="https://… for any S3-compatible store"
            placeholder="https://s3.fr-par.scw.cloud"
            value={form.endpoint}
            onChange={(v) => setForm((s) => ({ ...s, endpoint: v }))}
          />
          <Field
            label="region"
            hint="leave 'auto' for most providers"
            value={form.region}
            onChange={(v) => setForm((s) => ({ ...s, region: v }))}
          />
          <Field
            label="access key id"
            value={form.accessKeyId}
            onChange={(v) => setForm((s) => ({ ...s, accessKeyId: v }))}
          />
          <Field
            label="secret access key"
            hint={form.hasSecret ? "leave empty to keep current" : "required"}
            type="password"
            value={form.secretAccessKey ?? ""}
            onChange={(v) => setForm((s) => ({ ...s, secretAccessKey: v }))}
            last
          />
          <footer className="flex items-center justify-between gap-2 border-t border-[var(--border-subtle)] px-3 py-2">
            <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
              {settings.data?.hasSecret ? (
                <span className="inline-flex items-center gap-1">
                  <Check className="h-3 w-3 text-[var(--success)]" /> configured
                </span>
              ) : (
                "not configured"
              )}
            </span>
            <Button size="sm" type="submit" disabled={save.isPending}>
              <Save className="h-3.5 w-3.5" />
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </footer>
        </form>
      )}

      <p className="mt-4 font-mono text-[10px] text-[var(--text-tertiary)]">
        S3 credentials live here (admin only). Schedule backups per addon on its Backups tab
        (open the addon on the project canvas). Each backup job uses these credentials.
      </p>
    </div>
  );
}

function Field({
  label,
  hint,
  value,
  onChange,
  type = "text",
  placeholder,
  last,
}: {
  label: string;
  hint?: string;
  value: string;
  onChange: (v: string) => void;
  type?: "text" | "password";
  placeholder?: string;
  last?: boolean;
}) {
  const id = `backup-${label.replace(/\s+/g, "-")}`;
  return (
    <div
      className={
        "flex items-center gap-3 px-3 py-2" +
        (!last ? " border-b border-[var(--border-subtle)]" : "")
      }
    >
      <div className="min-w-[140px]">
        <label htmlFor={id} className="block text-[12px] text-[var(--text-secondary)]">
          {label}
        </label>
        {hint && (
          <div className="font-mono text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>
        )}
      </div>
      <Input
        id={id}
        type={type}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-7 max-w-[320px] font-mono text-[12px]"
        autoComplete="off"
        spellCheck={false}
      />
    </div>
  );
}
