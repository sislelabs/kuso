"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { Check, KeyRound, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";

// Project-level shared secrets card. Each row is one env var that
// gets auto-mounted into every service in the project via the
// "<project>-shared" Secret. Used for cross-service integrations
// like Resend, Postmark, Stripe, OpenAI — set once, every service
// gets it at boot.
//
// The integration tiles pre-fill the env var name (so the user
// doesn't have to remember whether it's RESEND_API_KEY or
// RESEND_TOKEN) but inline the value entry — no browser prompt
// dialog. Two flows otherwise share the same submit path.
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

  // editingKey is the env var name currently in the inline editor.
  // Set when the user clicks an integration tile or expands the
  // manual-add form. Empty = no editor open.
  const [editingKey, setEditingKey] = useState<string>("");
  const [editingValue, setEditingValue] = useState<string>("");

  const onSave = () => {
    const k = editingKey.trim();
    if (!k || !editingValue) return;
    if (!/^[A-Z][A-Z0-9_]*$/.test(k)) {
      toast.error("Use SCREAMING_SNAKE_CASE for env var names");
      return;
    }
    set.mutate(
      { key: k, value: editingValue },
      {
        onSuccess: () => {
          toast.success(`${k} saved to ${project}-shared`);
          setEditingKey("");
          setEditingValue("");
        },
      }
    );
  };

  const onCancel = () => {
    setEditingKey("");
    setEditingValue("");
  };

  const stored = (list.data?.keys ?? []).slice().sort();
  const has = (k: string) => stored.includes(k);

  return (
    <section className="space-y-4">
      <header>
        <h3 className="font-heading text-sm font-semibold tracking-tight">Project secrets</h3>
        <p className="mt-1 text-[12px] leading-relaxed text-[var(--text-secondary)]">
          Auto-attached to every service in this project as env vars (via{" "}
          <code className="rounded bg-[var(--bg-secondary)] px-1 font-mono text-[11px]">
            {project}-shared
          </code>
          ). Use for cross-service integrations like Resend, Postmark, Stripe, OpenAI.
        </p>
      </header>

      {/* Integration tiles — one click prefills the key + opens the
          inline value editor. Existing keys show a green checkmark. */}
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
        {INTEGRATIONS.map((it) => (
          <IntegrationTile
            key={it.envVar}
            name={it.name}
            envVar={it.envVar}
            description={it.description}
            existing={has(it.envVar)}
            onClick={() => {
              setEditingKey(it.envVar);
              setEditingValue("");
            }}
          />
        ))}
      </div>

      {/* Inline value editor (replaces the browser window.prompt) */}
      {editingKey && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSave();
          }}
          className="flex flex-col gap-2 rounded-md border border-[var(--border-strong)] bg-[var(--bg-secondary)]/60 p-3"
        >
          <div className="flex items-center gap-2">
            <KeyRound className="h-3 w-3 text-[var(--text-tertiary)]" />
            <span className="font-mono text-[12px] font-medium">{editingKey}</span>
            <button
              type="button"
              onClick={onCancel}
              className="ml-auto rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
              aria-label="Cancel"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          </div>
          <Input
            value={editingValue}
            onChange={(e) => setEditingValue(e.target.value)}
            type="password"
            placeholder="paste secret value"
            className="h-8 font-mono text-[12px]"
            spellCheck={false}
            autoComplete="new-password"
            autoFocus
          />
          <div className="flex items-center justify-end gap-2">
            <Button size="sm" type="submit" disabled={!editingValue || set.isPending}>
              <Plus className="h-3.5 w-3.5" />
              {set.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      )}

      {/* Stored list */}
      <section className="space-y-2">
        <header>
          <h4 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            stored ({stored.length})
          </h4>
        </header>
        {stored.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-6 text-center text-[12px] text-[var(--text-tertiary)]">
            No project secrets yet. Pick an integration above or add manually below.
          </p>
        ) : (
          <ul className="overflow-hidden rounded-md border border-[var(--border-subtle)]">
            {stored.map((k) => (
              <li
                key={k}
                className="flex items-center gap-2 border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 px-3 py-2 last:border-b-0 hover:bg-[var(--bg-secondary)]/70"
              >
                <KeyRound className="h-3 w-3 shrink-0 text-[var(--text-tertiary)]" />
                <span className="flex-1 truncate font-mono text-[12px] text-[var(--text-secondary)]">
                  {k}
                </span>
                <button
                  type="button"
                  onClick={() => {
                    if (!confirm(`Delete project secret ${k}?`)) return;
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

      {/* Manual add */}
      <section className="space-y-2">
        <header>
          <h4 className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
            add manually
          </h4>
        </header>
        <ManualAddRow
          onAdd={(k, v) =>
            set.mutate(
              { key: k, value: v },
              {
                onSuccess: () => {
                  toast.success(`${k} saved to ${project}-shared`);
                },
              }
            )
          }
          pending={set.isPending}
        />
      </section>
    </section>
  );
}

interface IntegrationDef {
  name: string;
  envVar: string;
  description: string;
}

const INTEGRATIONS: IntegrationDef[] = [
  { name: "Resend",   envVar: "RESEND_API_KEY",    description: "Transactional email" },
  { name: "Postmark", envVar: "POSTMARK_API_KEY",  description: "Transactional email" },
  { name: "Stripe",   envVar: "STRIPE_SECRET_KEY", description: "Payments" },
  { name: "OpenAI",   envVar: "OPENAI_API_KEY",    description: "LLM API" },
  { name: "Sentry",   envVar: "SENTRY_DSN",        description: "Error tracking" },
];

function IntegrationTile({
  name,
  envVar,
  description,
  existing,
  onClick,
}: {
  name: string;
  envVar: string;
  description: string;
  existing: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex flex-col items-start gap-0.5 rounded-md border px-3 py-2 text-left transition-colors",
        existing
          ? "border-emerald-500/30 bg-emerald-500/5 hover:bg-emerald-500/10"
          : "border-[var(--border-subtle)] bg-[var(--bg-secondary)]/40 hover:border-[var(--border-strong)] hover:bg-[var(--bg-tertiary)]/40"
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

function ManualAddRow({
  onAdd,
  pending,
}: {
  onAdd: (key: string, value: string) => void;
  pending: boolean;
}) {
  const [k, setK] = useState("");
  const [v, setV] = useState("");
  const submit = () => {
    const key = k.trim();
    if (!key || !v) return;
    if (!/^[A-Z][A-Z0-9_]*$/.test(key)) {
      toast.error("Use SCREAMING_SNAKE_CASE for env var names");
      return;
    }
    onAdd(key, v);
    setK("");
    setV("");
  };
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      className="flex items-center gap-2"
    >
      <Input
        value={k}
        onChange={(e) => setK(e.target.value)}
        placeholder="ENV_VAR_NAME"
        className="h-8 flex-1 font-mono text-[12px]"
        spellCheck={false}
        autoComplete="off"
      />
      <Input
        value={v}
        onChange={(e) => setV(e.target.value)}
        type="password"
        placeholder="value"
        className="h-8 flex-1 font-mono text-[12px]"
        spellCheck={false}
        autoComplete="new-password"
      />
      <Button size="sm" type="submit" disabled={!k.trim() || !v || pending}>
        <Plus className="h-3.5 w-3.5" />
      </Button>
    </form>
  );
}
