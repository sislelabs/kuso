"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { Check, Mail, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

// Project-level shared secrets card. Each row is one env var that
// gets auto-mounted into every service in the project via the
// "<project>-shared" Secret. Used for cross-service integrations
// like Resend, Postmark, Stripe, OpenAI — set once, every service
// gets it at boot.
//
// Server returns keys only; values are write-only. Two flows:
//   - manual upsert: type a key + value, click +
//   - integration tile: pre-fills the key (e.g. RESEND_API_KEY)
//     so users get the right shape without remembering the env var
//     name.
export function SharedSecretsCard({ project }: { project: string }) {
  const qc = useQueryClient();
  const list = useQuery<{ keys: string[] }>({
    queryKey: ["projects", project, "shared-secrets"],
    queryFn: () => api(`/api/projects/${encodeURIComponent(project)}/shared-secrets`),
  });
  const set = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api(`/api/projects/${encodeURIComponent(project)}/shared-secrets`, {
        method: "PUT",
        body: { key, value },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects", project, "shared-secrets"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });
  const unset = useMutation({
    mutationFn: (key: string) =>
      api(
        `/api/projects/${encodeURIComponent(project)}/shared-secrets/${encodeURIComponent(key)}`,
        { method: "DELETE" }
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects", project, "shared-secrets"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });

  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");

  const onSubmitManual = () => {
    if (!newKey.trim() || !newValue) return;
    set.mutate({ key: newKey.trim(), value: newValue });
    setNewKey("");
    setNewValue("");
    toast.success(`${newKey} saved to ${project}-shared`);
  };

  const onIntegration = (key: string) => {
    const value = window.prompt(`Paste your ${key} value:`);
    if (!value) return;
    set.mutate({ key, value });
    toast.success(`${key} saved to ${project}-shared — every service in this project gets it at boot`);
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Project secrets</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-xs text-[var(--text-secondary)]">
          Auto-attached to every service in this project as env vars (via{" "}
          <span className="font-mono">{project}-shared</span> kube Secret + envFromSecrets).
          Use for cross-service integrations like Resend, Postmark, Stripe.
        </p>

        {/* Integration tiles — one-click for the common cases */}
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          <IntegrationTile
            name="Resend"
            envVar="RESEND_API_KEY"
            description="Transactional email"
            existing={(list.data?.keys ?? []).includes("RESEND_API_KEY")}
            onAdd={() => onIntegration("RESEND_API_KEY")}
          />
          <IntegrationTile
            name="Postmark"
            envVar="POSTMARK_API_KEY"
            description="Transactional email"
            existing={(list.data?.keys ?? []).includes("POSTMARK_API_KEY")}
            onAdd={() => onIntegration("POSTMARK_API_KEY")}
          />
          <IntegrationTile
            name="Stripe"
            envVar="STRIPE_SECRET_KEY"
            description="Payments"
            existing={(list.data?.keys ?? []).includes("STRIPE_SECRET_KEY")}
            onAdd={() => onIntegration("STRIPE_SECRET_KEY")}
          />
          <IntegrationTile
            name="OpenAI"
            envVar="OPENAI_API_KEY"
            description="LLM API"
            existing={(list.data?.keys ?? []).includes("OPENAI_API_KEY")}
            onAdd={() => onIntegration("OPENAI_API_KEY")}
          />
          <IntegrationTile
            name="Sentry"
            envVar="SENTRY_DSN"
            description="Error tracking"
            existing={(list.data?.keys ?? []).includes("SENTRY_DSN")}
            onAdd={() => onIntegration("SENTRY_DSN")}
          />
        </div>

        {/* Existing keys */}
        <div className="space-y-1">
          <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            stored ({list.data?.keys.length ?? 0})
          </p>
          {(list.data?.keys ?? []).length === 0 ? (
            <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-3 text-center text-[11px] text-[var(--text-tertiary)]">
              No project secrets yet.
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
        </div>

        {/* Manual upsert */}
        <div className="space-y-1">
          <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            add manually
          </p>
          <div className="flex items-center gap-1">
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
              onKeyDown={(e) => {
                if (e.key === "Enter") onSubmitManual();
              }}
            />
            <Button
              size="sm"
              disabled={!newKey.trim() || !newValue || set.isPending}
              onClick={onSubmitManual}
            >
              <Plus className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function IntegrationTile({
  name,
  envVar,
  description,
  existing,
  onAdd,
}: {
  name: string;
  envVar: string;
  description: string;
  existing: boolean;
  onAdd: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onAdd}
      className={cn(
        "flex flex-col items-start gap-0.5 rounded-md border px-3 py-2 text-left transition-colors",
        existing
          ? "border-emerald-500/30 bg-emerald-500/5 hover:bg-emerald-500/10"
          : "border-[var(--border-subtle)] bg-[var(--bg-primary)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40"
      )}
    >
      <div className="flex w-full items-center gap-1.5">
        <span className="text-[12px] font-medium">{name}</span>
        {existing && <Check className="ml-auto h-3 w-3 text-emerald-400" />}
      </div>
      <p className="font-mono text-[10px] text-[var(--text-tertiary)]">{envVar}</p>
      <p className="text-[10px] text-[var(--text-tertiary)]">{description}</p>
    </button>
  );
}
