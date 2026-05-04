"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Globe, Plus, Trash2, KeyRound } from "lucide-react";
import { toast } from "sonner";

// /settings/instance-secrets — instance-wide env vars. Admin-only.
// Every service in every project gets these mounted via envFromSecrets
// alongside the per-project shared secret + addon conn secrets.
//
// Use case: API keys you want every service in every project to
// inherit (Sentry DSN, Datadog token, OpenAI proxy creds, etc.).
// One canonical place to rotate them.
//
// We hide keys that look like INSTANCE_ADDON_<UPPER>_DSN_ADMIN —
// those are managed by the dedicated /settings/instance-addons UI
// and surface as a connection record there, not as a raw env var
// here. Keeping them out of this page avoids a footgun where an
// admin deletes the addon admin DSN by mistake.
function isInstanceAddonAdminKey(k: string): boolean {
  return /^INSTANCE_ADDON_[A-Z0-9_]+_DSN_ADMIN$/.test(k);
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
  const [newValue, setNewValue] = useState("");

  if (!isAdmin) {
    return (
      <div className="mx-auto max-w-3xl p-6 lg:p-8">
        <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          Instance-wide secrets are admin-only.
        </p>
      </div>
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
    set.mutate(
      { key: k, value: newValue },
      {
        onSuccess: () => {
          toast.success(`${k} saved`);
          setNewKey("");
          setNewValue("");
        },
      }
    );
  };

  const visibleKeys = (list.data?.keys ?? [])
    .filter((k) => !isInstanceAddonAdminKey(k))
    .sort();

  return (
    <div className="mx-auto max-w-3xl p-6 lg:p-8">
      <header className="mb-6 flex items-start gap-3">
        <Globe className="mt-1 h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Instance secrets</h1>
          <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
            Auto-mounted into every service in every project as env vars (via{" "}
            <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">
              kuso-instance-shared
            </code>
            ). Use for cluster-wide integrations like Sentry, Datadog, observability proxies. Per-project
            secrets live under each project&apos;s settings; per-service vars live on the service itself.
          </p>
        </div>
      </header>

      {/* Existing keys — flat list, no Card chrome */}
      <section className="space-y-3">
        <header className="flex items-baseline justify-between">
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            stored ({visibleKeys.length})
          </h2>
        </header>
        {list.isPending ? (
          <Skeleton className="h-16 w-full" />
        ) : visibleKeys.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-8 text-center text-[12px] text-[var(--text-tertiary)]">
            No instance secrets yet. Add one below.
          </p>
        ) : (
          <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
            {visibleKeys.map((k) => (
              <li
                key={k}
                className="flex items-center gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2 last:border-b-0 hover:bg-[var(--bg-secondary)]/70"
              >
                <KeyRound className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
                <span className="flex-1 truncate font-mono text-[12px] text-[var(--text-secondary)]">{k}</span>
                <span className="font-mono text-[10px] text-[var(--text-tertiary)]">value hidden</span>
                <button
                  type="button"
                  onClick={() => {
                    if (!confirm(`Delete instance secret ${k}?`)) return;
                    unset.mutate(k);
                  }}
                  disabled={unset.isPending}
                  className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                  aria-label={`Delete ${k}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {/* Add new */}
      <section className="mt-8 space-y-3">
        <header>
          <h2 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            add or update
          </h2>
        </header>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSubmit();
          }}
          className="flex flex-col gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 p-3"
        >
          <div className="flex flex-col gap-2 sm:flex-row">
            <div className="flex-1">
              <label
                htmlFor="instance-secret-key"
                className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
              >
                key
              </label>
              <Input
                id="instance-secret-key"
                value={newKey}
                onChange={(e) => setNewKey(e.target.value)}
                placeholder="SENTRY_DSN"
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoComplete="off"
              />
            </div>
            <div className="flex-[2]">
              <label
                htmlFor="instance-secret-value"
                className="mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]"
              >
                value
              </label>
              <Input
                id="instance-secret-value"
                value={newValue}
                onChange={(e) => setNewValue(e.target.value)}
                type="password"
                placeholder="paste secret value"
                className="h-8 font-mono text-[12px]"
                spellCheck={false}
                autoComplete="new-password"
              />
            </div>
          </div>
          <div className="flex items-center justify-between gap-2">
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              SCREAMING_SNAKE_CASE. Updates overwrite the existing value silently.
            </p>
            <Button
              size="sm"
              type="submit"
              disabled={!newKey.trim() || !newValue || set.isPending}
            >
              <Plus className="h-3.5 w-3.5" />
              {set.isPending ? "Saving…" : "Save secret"}
            </Button>
          </div>
        </form>
      </section>
    </div>
  );
}
