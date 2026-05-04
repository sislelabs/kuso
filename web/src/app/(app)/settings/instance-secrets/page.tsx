"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { api } from "@/lib/api-client";
import { Globe, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

// /settings/instance-secrets — instance-wide env vars. Admin-only.
// Every service in every project gets these mounted via envFromSecrets
// alongside the per-project shared secret + addon conn secrets.
//
// Use case: API keys you want every service in every project to
// inherit (Sentry DSN, Datadog token, OpenAI proxy creds, etc.).
// One canonical place to rotate them.
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
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-4 text-sm text-amber-400">
          Instance-wide secrets are admin-only.
        </p>
      </div>
    );
  }

  const onSubmit = () => {
    if (!newKey.trim() || !newValue) return;
    set.mutate({ key: newKey.trim(), value: newValue });
    toast.success(`${newKey} set`);
    setNewKey("");
    setNewValue("");
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6 lg:p-8">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">
            Instance secrets
          </h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Auto-mounted into every service in every project. Use for cluster-wide
            integrations (Sentry, Datadog, observability proxies). Per-project
            secrets live under each project&apos;s settings; per-service vars live
            on the service itself.
          </p>
        </div>
        <Globe className="h-6 w-6 shrink-0 text-[var(--text-tertiary)]" />
      </header>

      <Card>
        <CardHeader>
          <CardTitle>Stored ({list.data?.keys.length ?? 0})</CardTitle>
        </CardHeader>
        <CardContent>
          {list.isPending ? (
            <Skeleton className="h-16 w-full" />
          ) : (list.data?.keys ?? []).length === 0 ? (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
              No instance secrets yet.
            </p>
          ) : (
            <ul className="divide-y divide-[var(--border-subtle)] rounded-md border border-[var(--border-subtle)]">
              {(list.data?.keys ?? []).sort().map((k) => (
                <li key={k} className="flex items-center gap-2 px-3 py-1.5">
                  <span className="flex-1 truncate font-mono text-[12px]">{k}</span>
                  <button
                    type="button"
                    onClick={() => unset.mutate(k)}
                    disabled={unset.isPending}
                    className="rounded p-1 text-[var(--text-tertiary)] hover:bg-red-500/10 hover:text-red-400 disabled:opacity-40"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Add</CardTitle>
        </CardHeader>
        <CardContent>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              onSubmit();
            }}
            className="flex items-center gap-1"
          >
            <Input
              value={newKey}
              onChange={(e) => setNewKey(e.target.value)}
              placeholder="ENV_VAR_NAME"
              className="h-8 flex-1 font-mono text-[12px]"
            />
            <Input
              value={newValue}
              onChange={(e) => setNewValue(e.target.value)}
              type="password"
              placeholder="value"
              className="h-8 flex-1 font-mono text-[12px]"
            />
            <Button size="sm" type="submit" disabled={!newKey.trim() || !newValue || set.isPending}>
              <Plus className="h-3.5 w-3.5" />
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
