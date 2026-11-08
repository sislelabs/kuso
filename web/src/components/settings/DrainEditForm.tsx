"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DRAIN_MASK,
  buildDrainUpdate,
  drainToEditForm,
  urlHasCredentials,
  useUpdateDrain,
  type Drain,
  type DrainEditForm as DrainEditFormState,
  type DrainType,
} from "@/features/drains";
import { SettingsField, SettingsSelect } from "./SettingsSection";

// DrainEditForm edits one drain in place. Stored header values and the
// HMAC secret come back masked; leaving them alone keeps the originals.
export function DrainEditForm({
  drain,
  typeLabels,
  onDone,
}: {
  drain: Drain;
  typeLabels: Record<DrainType, string>;
  onDone: () => void;
}) {
  const update = useUpdateDrain();
  const [f, setF] = useState<DrainEditFormState>(() => drainToEditForm(drain));
  const set = <K extends keyof DrainEditFormState>(k: K, v: DrainEditFormState[K]) =>
    setF((cur) => ({ ...cur, [k]: v }));
  const id = (k: string) => `drain-edit-${drain.id}-${k}`;

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const r = buildDrainUpdate(drain, f);
    if (!r.ok) {
      toast.error(r.error);
      return;
    }
    update.mutate(
      { id: drain.id, body: r.body },
      {
        onSuccess: (d) => {
          toast.success(`Drain ${d.name} saved — applies within ~30s`);
          onDone();
        },
        onError: (err) => toast.error(err instanceof Error ? err.message : "Save failed"),
      }
    );
  };

  return (
    <form
      onSubmit={onSubmit}
      className="space-y-5 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]/40 p-4 sm:p-5"
    >
      <div className="grid gap-5 sm:grid-cols-[12rem_1fr]">
        <SettingsField label="Type" htmlFor={id("type")}>
          <SettingsSelect
            id={id("type")}
            value={f.type}
            onChange={(e) => set("type", e.target.value as DrainType)}
          >
            {(Object.keys(typeLabels) as DrainType[]).map((t) => (
              <option key={t} value={t}>{typeLabels[t]}</option>
            ))}
          </SettingsSelect>
        </SettingsField>
        <SettingsField label="URL" htmlFor={id("url")}>
          <Input
            id={id("url")}
            // Stored URL carries a token in its userinfo: keep it hidden.
            type={urlHasCredentials(drain.url) ? "password" : "text"}
            value={f.url}
            onChange={(e) => set("url", e.target.value)}
            className="font-mono"
            spellCheck={false}
            autoComplete="off"
          />
        </SettingsField>
      </div>
      <div className="grid gap-5 sm:grid-cols-2">
        <SettingsField label="Name" htmlFor={id("name")}>
          <Input id={id("name")} value={f.name} onChange={(e) => set("name", e.target.value)} />
        </SettingsField>
        <SettingsField label="Project" htmlFor={id("project")} hint="Leave blank to drain every project.">
          <Input
            id={id("project")}
            value={f.project}
            onChange={(e) => set("project", e.target.value)}
            placeholder="All projects"
            className="font-mono"
            spellCheck={false}
          />
        </SettingsField>
      </div>
      <SettingsField
        label="Headers"
        htmlFor={id("headers")}
        hint={
          <>
            One per line. <span className="font-mono">{DRAIN_MASK}</span> keeps the stored value;
            delete a line to remove that header.
          </>
        }
      >
        <textarea
          id={id("headers")}
          value={f.headersText}
          onChange={(e) => set("headersText", e.target.value)}
          rows={2}
          spellCheck={false}
          className="w-full resize-y rounded-sm border border-border bg-input px-3 py-2 font-mono text-xs text-[var(--text-primary)] outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
        />
      </SettingsField>
      <SettingsField label="HMAC secret" htmlFor={id("secret")}>
        <Input
          id={id("secret")}
          type="password"
          value={f.secret}
          onChange={(e) => set("secret", e.target.value)}
          placeholder={drain.secret ? "Unchanged" : "None"}
          disabled={f.clearSecret}
          className="font-mono"
          autoComplete="new-password"
        />
      </SettingsField>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
        {drain.secret && (
          <label className="flex items-center gap-1.5 text-sm text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={f.clearSecret}
              onChange={(e) => set("clearSecret", e.target.checked)}
            />
            Remove secret
          </label>
        )}
        <label className="flex items-center gap-1.5 text-sm text-[var(--text-secondary)]">
          <input type="checkbox" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          Enabled
        </label>
      </div>
      <div className="flex justify-end gap-2 border-t border-[var(--border-subtle)] pt-4">
        <Button size="sm" variant="ghost" type="button" onClick={onDone} disabled={update.isPending}>
          Cancel
        </Button>
        <Button size="sm" type="submit" disabled={!f.url.trim() || update.isPending}>
          {update.isPending ? "Saving…" : "Save drain"}
        </Button>
      </div>
    </form>
  );
}
