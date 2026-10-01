"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  useSharedSecrets,
  useSetSharedSecret,
  useUnsetSharedSecret,
} from "@/features/project-secrets";
import { Check, KeyRound, Plus, Trash2, X } from "lucide-react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { ApiError } from "@/lib/api-client";

// Project-level shared secrets card. Each row is one env var in the
// "<project>-shared" Secret; services opt in per key from their
// Variables tab. Used for cross-service integrations
// like Resend, Postmark, Stripe, OpenAI — set once, every service
// gets it at boot.
//
// The integration tiles pre-fill the env var name (so the user
// doesn't have to remember whether it's RESEND_API_KEY or
// RESEND_TOKEN) but inline the value entry — no browser prompt
// dialog. Two flows otherwise share the same submit path.
export function SharedSecretsCard({ project }: { project: string }) {
  const list = useSharedSecrets(project);
  const set = useSetSharedSecret(project);
  const unset = useUnsetSharedSecret(project);

  // editingKey is the env var name currently in the inline editor.
  // Set when the user clicks an integration tile or expands the
  // manual-add form. Empty = no editor open.
  const [editingKey, setEditingKey] = useState<string>("");
  const [editingValue, setEditingValue] = useState<string>("");
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);
  const [pendingOverwrite, setPendingOverwrite] = useState<string | null>(null);

  // A 409 "shadowed" means some service sets the same key itself, so
  // the shared value wouldn't reach it. Offer an explicit force instead
  // of a dead-end error toast.
  const [pendingShadowed, setPendingShadowed] = useState<{ key: string; value: string; message: string } | null>(null);
  const submit = (k: string, v: string, force: boolean, after?: () => void) => {
    set.mutate(
      { key: k, value: v, ...(force ? { force: true } : {}) },
      {
        onSuccess: () => {
          toast.success(`${k} saved to ${project}-shared`, {
            description: "Subscribed services pick it up on their next restart or deploy.",
          });
          setPendingShadowed(null);
          after?.();
        },
        onError: (e) => {
          if (!force && e instanceof ApiError && e.status === 409 && e.code === "shadowed") {
            setPendingShadowed({ key: k, value: v, message: e.message });
            return;
          }
          setPendingShadowed(null);
          toast.error(e instanceof Error ? e.message : `Failed to save ${k}`);
        },
      }
    );
  };

  const doSave = (k: string) =>
    submit(k, editingValue, false, () => {
      setEditingKey("");
      setEditingValue("");
      setPendingOverwrite(null);
    });

  const onSave = () => {
    const k = editingKey.trim();
    if (!k || !editingValue) return;
    if (!/^[A-Z][A-Z0-9_]*$/.test(k)) {
      toast.error("Use SCREAMING_SNAKE_CASE for env var names");
      return;
    }
    // Overwriting an existing shared key changes the value for every
    // service subscribed to it — those pods pick up the new value on
    // their next restart. Confirm before clobbering; adding a brand-new
    // key is friction-free.
    if (stored.includes(k)) {
      setPendingOverwrite(k);
      return;
    }
    doSave(k);
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
          Set a value once, then pick it from any service&apos;s Variables tab under Add → Project
          secret.
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
                  onClick={() => setPendingDelete(k)}
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
          onAdd={(k, v) => submit(k, v, false)}
          pending={set.isPending}
        />
      </section>

      <ConfirmDialog
        open={pendingShadowed !== null}
        title={`Save ${pendingShadowed?.key ?? ""} anyway?`}
        body={
          <p>
            {pendingShadowed?.message} Services that set this key themselves keep
            their own value; the rest get the shared one.
          </p>
        }
        confirmLabel="Save anyway"
        destructive={false}
        pending={set.isPending}
        onConfirm={() => {
          if (!pendingShadowed) return;
          const { key, value } = pendingShadowed;
          submit(key, value, true, () => {
            if (editingKey.trim() === key) {
              setEditingKey("");
              setEditingValue("");
            }
          });
        }}
        onCancel={() => setPendingShadowed(null)}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title="Delete shared secret?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">
              {pendingDelete}
            </span>{" "}
            is removed from every service that uses it. A service that reads it
            will fail on its next restart.
          </p>
        }
        confirmLabel="Delete shared secret"
        destructive
        pending={unset.isPending}
        onConfirm={() => {
          if (pendingDelete) unset.mutate(pendingDelete);
          setPendingDelete(null);
        }}
        onCancel={() => setPendingDelete(null)}
      />

      <ConfirmDialog
        open={pendingOverwrite !== null}
        title="Overwrite shared secret?"
        body={
          <p>
            <span className="font-mono text-[var(--text-primary)]">
              {pendingOverwrite}
            </span>{" "}
            already exists. The new value reaches every service that uses it on
            that service&apos;s next restart.
          </p>
        }
        confirmLabel="Overwrite"
        destructive
        pending={set.isPending}
        onConfirm={() => {
          if (pendingOverwrite) doSave(pendingOverwrite);
        }}
        onCancel={() => setPendingOverwrite(null)}
      />
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
      <Button size="sm" type="submit" aria-label="Add secret" disabled={!k.trim() || !v || pending}>
        <Plus className="h-3.5 w-3.5" />
      </Button>
    </form>
  );
}
