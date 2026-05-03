"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { useCan, Perms } from "@/features/auth";
import { toast } from "sonner";
import { Settings as SettingsIcon, Save } from "lucide-react";

interface SettingsResp {
  settings: Record<string, unknown>;
}

const FIELDS: { key: string; label: string; hint: string; placeholder?: string }[] = [
  { key: "baseDomain",       label: "Base domain",         hint: "default suffix for service URLs (e.g. apps.example.com)", placeholder: "kuso.sislelabs.com" },
  { key: "clusterIssuer",    label: "ClusterIssuer",       hint: "cert-manager issuer used for auto-TLS",                  placeholder: "letsencrypt-prod" },
  { key: "ingressClass",     label: "Ingress class",       hint: "ingressClassName the chart stamps on each Ingress",      placeholder: "traefik" },
  { key: "letsEncryptEmail", label: "Let's Encrypt email", hint: "operator contact for cert renewal warnings",             placeholder: "you@example.com" },
];

export default function ClusterConfigPage() {
  const canEdit = useCan(Perms.SettingsAdmin);
  const qc = useQueryClient();
  const settings = useQuery({
    queryKey: ["admin", "settings"],
    queryFn: async () => (await api<SettingsResp>("/api/config")).settings ?? {},
  });
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [rawJson, setRawJson] = useState("");
  const [rawError, setRawError] = useState<string | null>(null);

  useEffect(() => {
    if (settings.data) {
      setDraft(settings.data);
      setRawJson(JSON.stringify(settings.data, null, 2));
    }
  }, [settings.data]);

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      api("/api/config", { method: "POST", body }),
    onSuccess: () => {
      toast.success("Cluster config saved");
      qc.invalidateQueries({ queryKey: ["admin", "settings"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  if (settings.isPending) {
    return (
      <div className="mx-auto max-w-2xl p-6 lg:p-8">
        <Skeleton className="h-72 rounded-md" />
      </div>
    );
  }

  const setField = (key: string, val: string) => {
    setDraft((cur) => {
      const next = { ...cur, [key]: val };
      setRawJson(JSON.stringify(next, null, 2));
      return next;
    });
  };

  const onSaveFriendly = () => {
    if (!canEdit) {
      toast.error("settings:admin permission required");
      return;
    }
    save.mutate(draft);
  };
  const onSaveRaw = () => {
    if (!canEdit) {
      toast.error("settings:admin permission required");
      return;
    }
    try {
      const parsed = JSON.parse(rawJson);
      if (typeof parsed !== "object" || parsed === null) {
        setRawError("must be a JSON object");
        return;
      }
      setRawError(null);
      setDraft(parsed);
      save.mutate(parsed);
    } catch (e) {
      setRawError(e instanceof Error ? e.message : "invalid JSON");
    }
  };

  return (
    <div className="mx-auto max-w-2xl space-y-4 p-6 lg:p-8">
      <header className="flex items-center gap-3">
        <SettingsIcon className="h-5 w-5 text-[var(--text-tertiary)]" />
        <div>
          <h1 className="font-heading text-xl font-semibold tracking-tight">Cluster config</h1>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Cluster-wide knobs stored on the Kuso CR. Read by the server on every reload.
          </p>
        </div>
      </header>

      {!canEdit && (
        <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 font-mono text-[10px] text-amber-400">
          You don&apos;t have <span className="text-[var(--text-secondary)]">settings:admin</span>.
          The form is read-only.
        </p>
      )}

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Common settings</h2>
        </header>
        <div className="space-y-3 p-4">
          {FIELDS.map((f) => (
            <div key={f.key} className="grid grid-cols-[140px_1fr] items-start gap-3">
              <div>
                <div className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
                  {f.label}
                </div>
                <div className="mt-0.5 text-[10px] text-[var(--text-tertiary)]/70">{f.hint}</div>
              </div>
              <Input
                value={String(draft[f.key] ?? "")}
                onChange={(e) => setField(f.key, e.target.value)}
                placeholder={f.placeholder}
                className="h-8 font-mono text-[12px]"
                disabled={!canEdit}
                spellCheck={false}
              />
            </div>
          ))}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" onClick={onSaveFriendly} disabled={!canEdit || save.isPending}>
            <Save className="h-3 w-3" />
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </footer>
      </section>

      <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
        <header className="border-b border-[var(--border-subtle)] px-4 py-2.5">
          <h2 className="text-sm font-semibold tracking-tight">Raw spec</h2>
          <p className="mt-0.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            Direct edit of the entire Kuso CR spec. Validates JSON before save.
          </p>
        </header>
        <div className="p-3">
          <textarea
            value={rawJson}
            onChange={(e) => setRawJson(e.target.value)}
            spellCheck={false}
            rows={Math.min(28, Math.max(8, rawJson.split("\n").length))}
            disabled={!canEdit}
            className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)] disabled:opacity-50"
          />
          {rawError && (
            <p className="mt-2 font-mono text-[10px] text-red-400">parse error: {rawError}</p>
          )}
        </div>
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setRawJson(JSON.stringify(draft, null, 2))}
            disabled={!canEdit}
          >
            Sync from form
          </Button>
          <Button size="sm" onClick={onSaveRaw} disabled={!canEdit || save.isPending}>
            <Save className="h-3 w-3" />
            {save.isPending ? "Saving…" : "Save raw"}
          </Button>
        </footer>
      </section>
    </div>
  );
}
