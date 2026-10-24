"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DRAIN_MASK,
  buildDrainUpdate,
  drainToEditForm,
  useUpdateDrain,
  type Drain,
  type DrainEditForm as DrainEditFormState,
  type DrainType,
} from "@/features/drains";

const labelCls = "mb-1 block font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]";

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
    <form onSubmit={onSubmit} className="flex flex-col gap-3 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
      <div className="flex flex-col gap-2 sm:flex-row">
        <div className="sm:w-40">
          <label htmlFor={id("type")} className={labelCls}>type</label>
          <select
            id={id("type")}
            value={f.type}
            onChange={(e) => set("type", e.target.value as DrainType)}
            className="h-8 w-full rounded-sm border border-[var(--border-subtle)] bg-[var(--input)] px-2 font-mono text-[12px]"
          >
            {(Object.keys(typeLabels) as DrainType[]).map((t) => (
              <option key={t} value={t}>{typeLabels[t]}</option>
            ))}
          </select>
        </div>
        <div className="flex-1">
          <label htmlFor={id("url")} className={labelCls}>url</label>
          <Input
            id={id("url")}
            value={f.url}
            onChange={(e) => set("url", e.target.value)}
            className="h-8 font-mono text-[12px]"
            spellCheck={false}
            autoComplete="off"
          />
        </div>
      </div>
      <div className="flex flex-col gap-2 sm:flex-row">
        <div className="flex-1">
          <label htmlFor={id("name")} className={labelCls}>name</label>
          <Input id={id("name")} value={f.name} onChange={(e) => set("name", e.target.value)} className="h-8 text-[12px]" />
        </div>
        <div className="flex-1">
          <label htmlFor={id("project")} className={labelCls}>project (blank = all)</label>
          <Input
            id={id("project")}
            value={f.project}
            onChange={(e) => set("project", e.target.value)}
            placeholder="all projects"
            className="h-8 font-mono text-[12px]"
            spellCheck={false}
          />
        </div>
      </div>
      <div>
        <label htmlFor={id("headers")} className={labelCls}>headers (one per line)</label>
        <textarea
          id={id("headers")}
          value={f.headersText}
          onChange={(e) => set("headersText", e.target.value)}
          rows={2}
          spellCheck={false}
          className="w-full resize-y rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
        />
        <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {DRAIN_MASK} keeps the stored value; delete a line to remove that header.
        </p>
      </div>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-end">
        <div className="flex-1">
          <label htmlFor={id("secret")} className={labelCls}>hmac secret</label>
          <Input
            id={id("secret")}
            type="password"
            value={f.secret}
            onChange={(e) => set("secret", e.target.value)}
            placeholder={drain.secret ? "unchanged" : "none"}
            disabled={f.clearSecret}
            className="h-8 font-mono text-[12px]"
            autoComplete="new-password"
          />
        </div>
        {drain.secret && (
          <label className="flex h-8 items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
            <input
              type="checkbox"
              checked={f.clearSecret}
              onChange={(e) => set("clearSecret", e.target.checked)}
            />
            remove secret
          </label>
        )}
        <label className="flex h-8 items-center gap-1.5 font-mono text-[11px] text-[var(--text-secondary)]">
          <input type="checkbox" checked={f.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          enabled
        </label>
      </div>
      <div className="flex justify-end gap-2">
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
