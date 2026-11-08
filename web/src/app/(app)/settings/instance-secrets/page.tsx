"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Plus, Trash2, KeyRound } from "lucide-react";
import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsField, SettingsNotice, SettingsSection } from "@/components/settings/SettingsSection";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { EmptyState } from "@/components/shared/EmptyState";
import { QueryErrorState } from "@/components/shared/QueryErrorState";

// /settings/instance-secrets — instance-wide env vars. Admin-only.
// Every service in every project gets these mounted via envFromSecrets
// alongside the per-project shared secret + addon conn secrets.
//
// Use case: API keys you want every service in every project to
// inherit (Sentry DSN, Datadog token, OpenAI proxy creds, etc.).
// One canonical place to rotate them.
//
// We hide keys that look like INSTANCE_ADDON_<UPPER>_DSN_ADMIN —
// those are managed by the Cluster database UI (/settings/database,
// "Additional shared servers") and surface as a connection record
// there, not as a raw env var here. Keeping them out of this page
// avoids a footgun where an
// admin deletes the addon admin DSN by mistake.
function isInstanceAddonAdminKey(k: string): boolean {
  return /^INSTANCE_ADDON_[A-Z0-9_-]+_DSN_ADMIN$/.test(k);
}

export default function InstanceSecretsPage() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const list = useQuery<{ keys: string[] }>({
    queryKey: ["instance-secrets"],
    queryFn: () => api("/api/instance-secrets"),
    enabled: isAdmin,
  });
  const set = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api("/api/instance-secrets", { method: "PUT", body: { key, value } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-secrets"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });
  const unset = useMutation({
    mutationFn: (key: string) =>
      api(`/api/instance-secrets/${encodeURIComponent(key)}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["instance-secrets"] }),
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  const [newKey, setNewKey] = useState("");
  // Typed-name confirm for an instance-secret delete. The blast
  // radius is huge — every workload in every project mounts these —
  // so a stray click on the trash icon shouldn't be enough.
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [newValue, setNewValue] = useState("");
  // Saving is an upsert, so replacing a stored key asks first.
  const [pendingOverwrite, setPendingOverwrite] = useState<string | null>(null);

  if (!isAdmin) {
    return (
      <SettingsPage title="Instance secrets">
        <SettingsNotice tone="warning">Instance-wide secrets are admin-only (settings:admin).</SettingsNotice>
      </SettingsPage>
    );
  }

  const onSubmit = () => {
    const k = newKey.trim();
    if (!k || !newValue) return;
    if (!/^[A-Z][A-Z0-9_]*$/.test(k)) {
      toast.error("Use SCREAMING_SNAKE_CASE for env var names");
      return;
    }
    if (isInstanceAddonAdminKey(k)) {
      toast.error("Use Instance addons → Register for INSTANCE_ADDON_*_DSN_ADMIN keys");
      return;
    }
    if (!list.isSuccess) return;
    if (list.data.keys.includes(k)) {
      setPendingOverwrite(k);
      return;
    }
    save(k);
  };

  const save = (k: string) =>
    set.mutate(
      { key: k, value: newValue },
      {
        onSuccess: () => {
          toast.success(`${k} saved`);
          setNewKey("");
          setNewValue("");
          setPendingOverwrite(null);
        },
      }
    );

  const visibleKeys = (list.data?.keys ?? [])
    .filter((k) => !isInstanceAddonAdminKey(k))
    .sort();

  return (
    <SettingsPage
      title="Instance secrets"
      description={
        <>
          Env vars mounted into every service in every project (via{" "}
          <code className="rounded bg-[var(--bg-tertiary)] px-1 font-mono text-xs text-[var(--text-primary)]">
            kuso-instance-shared
          </code>
          ). Use for cluster-wide integrations like Sentry or Datadog. Per-project secrets live in project settings;
          per-service vars live on the service.
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onSubmit();
        }}
      >
        <SettingsSection
          title="Add or update"
          footerHint="Saving an existing key asks before replacing it. Services pick up changes on their next restart."
          footer={
            <Button
              size="sm"
              type="submit"
              disabled={!newKey.trim() || !newValue || set.isPending || !list.isSuccess}
            >
              <Plus className="h-3.5 w-3.5" />
              {set.isPending ? "Saving…" : "Save secret"}
            </Button>
          }
        >
          <div className="grid gap-5 sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
            <SettingsField label="Key" htmlFor="instance-secret-key" hint="SCREAMING_SNAKE_CASE.">
              <Input
                id="instance-secret-key"
                value={newKey}
                onChange={(e) => setNewKey(e.target.value)}
                placeholder="e.g. SENTRY_DSN"
                className="font-mono"
                spellCheck={false}
                autoComplete="off"
              />
            </SettingsField>
            <SettingsField label="Value" htmlFor="instance-secret-value" hint="Write-only: it is never shown again.">
              <Input
                id="instance-secret-value"
                value={newValue}
                onChange={(e) => setNewValue(e.target.value)}
                type="password"
                placeholder="Paste the secret value"
                className="font-mono"
                spellCheck={false}
                autoComplete="new-password"
              />
            </SettingsField>
          </div>
        </SettingsSection>
      </form>

      <SettingsSection
        title="Stored secrets"
        description={`${visibleKeys.length} ${visibleKeys.length === 1 ? "key" : "keys"}. Values are write-only.`}
        flush
      >
        {list.isPending ? (
          <Skeleton className="m-4 h-16" />
        ) : list.isError ? (
          <QueryErrorState
            what="instance secrets"
            error={list.error}
            onRetry={() => void list.refetch()}
            className="m-4"
          />
        ) : visibleKeys.length === 0 ? (
          <EmptyState title="No instance secrets yet" description="Add one above." className="px-3 py-8" />
        ) : (
          <ul className="divide-y divide-[var(--border-subtle)]">
            {visibleKeys.map((k) => (
              <li key={k} className="flex items-center gap-3 px-4 py-2.5 sm:px-5">
                <KeyRound className="h-3.5 w-3.5 shrink-0 text-[var(--text-tertiary)]" />
                <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-[var(--text-primary)]">{k}</span>
                <span className="hidden text-xs text-[var(--text-tertiary)] sm:inline">Value hidden</span>
                <button
                  type="button"
                  onClick={() => setPendingDelete(k)}
                  disabled={unset.isPending}
                  className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--error-subtle)] hover:text-[var(--error)] disabled:opacity-40"
                  aria-label={`Delete ${k}`}
                  title="Delete"
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </SettingsSection>

      <ConfirmDialog
        open={pendingOverwrite !== null}
        title="Replace instance secret?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">{pendingOverwrite}</span>{" "}
            already exists. This replaces the value mounted on every workload in every
            project; each picks it up on its next restart.
          </p>
        }
        confirmLabel="Replace"
        destructive
        pending={set.isPending}
        onConfirm={() => {
          if (pendingOverwrite) save(pendingOverwrite);
        }}
        onCancel={() => setPendingOverwrite(null)}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title="Delete instance secret?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">
              {pendingDelete}
            </span>{" "}
            is mounted on every workload in every project. Removing it will
            cause services that read the variable to crash on next restart
            until they're updated. This cannot be undone — you'll need to
            re-enter the value to restore.
          </p>
        }
        typeToConfirm={pendingDelete ?? undefined}
        confirmLabel="Delete instance secret"
        destructive
        pending={unset.isPending}
        onConfirm={() => {
          if (pendingDelete) unset.mutate(pendingDelete);
          setPendingDelete(null);
        }}
        onCancel={() => setPendingDelete(null)}
      />
    </SettingsPage>
  );
}
