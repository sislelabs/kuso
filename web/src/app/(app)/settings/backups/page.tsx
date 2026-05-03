"use client";

import { useEffect, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Save, HardDrive, Check } from "lucide-react";

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
            S3-compatible storage for scheduled addon dumps. Every postgres addon with a
            <span className="font-mono"> backup.schedule</span> ships dumps here.
          </p>
        </div>
      </header>

      {settings.isPending ? (
        <Skeleton className="h-72 w-full rounded-md" />
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
                  <Check className="h-3 w-3 text-emerald-400" /> configured
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
        Add <span className="text-[var(--text-secondary)]">backup: {`{ schedule: "0 3 * * *" }`}</span> on a postgres addon
        in kuso.yml to enable daily dumps. The CronJob inherits these credentials.
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
  return (
    <div
      className={
        "flex items-center gap-3 px-3 py-2" +
        (!last ? " border-b border-[var(--border-subtle)]" : "")
      }
    >
      <div className="min-w-[140px]">
        <div className="text-[12px] text-[var(--text-secondary)]">{label}</div>
        {hint && (
          <div className="font-mono text-[10px] text-[var(--text-tertiary)]/70">{hint}</div>
        )}
      </div>
      <Input
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
