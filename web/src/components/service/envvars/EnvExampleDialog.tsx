"use client";

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Callout } from "@/components/ui/callout";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { VarBadge } from "@/components/service/EnvVarParts";
import { isSensitiveVar } from "@/components/service/envvars/sensitivity";
import {
  envExampleQueryKey,
  getEnvExample,
  pickEnvExampleEntries,
  planEnvExampleImport,
} from "@/features/services/envExample";
import { describeError } from "@/lib/errors";
import { cn } from "@/lib/utils";

// EnvExampleDialog previews the repo's .env.example and stages the picked
// keys as unsaved rows. Keys the service already has are listed but never
// selectable, so an import can't overwrite anything.
export function EnvExampleDialog({
  open,
  onOpenChange,
  project,
  service,
  existingNames,
  onImport,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  project: string;
  service: string;
  existingNames: ReadonlySet<string>;
  onImport: (entries: { name: string; value: string }[]) => void;
}) {
  const q = useQuery({
    queryKey: envExampleQueryKey(project, service),
    queryFn: () => getEnvExample(project, service),
    enabled: open,
    staleTime: 60_000,
    retry: false,
  });
  const example = q.data?.kind === "ok" ? q.data.example : undefined;
  const rows = useMemo(
    () => planEnvExampleImport(example?.entries ?? [], existingNames),
    [example, existingNames],
  );
  const [selected, setSelected] = useState<Set<string>>(new Set());
  // Every new key starts ticked each time the preview (re)loads.
  useEffect(() => {
    if (open) setSelected(new Set(rows.filter((r) => !r.exists).map((r) => r.key)));
  }, [open, rows]);

  const fresh = rows.filter((r) => !r.exists);
  const picked = pickEnvExampleEntries(rows, selected, existingNames);
  const toggle = (key: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  const allOn = fresh.length > 0 && fresh.every((r) => selected.has(r.key));

  let body: ReactNode;
  if (q.isPending) {
    body = (
      <div className="space-y-2" aria-busy="true" aria-label="Reading the repository">
        {Array.from({ length: 4 }, (_, i) => (
          <div key={i} className="h-8 animate-pulse rounded-md bg-[var(--bg-secondary)] motion-reduce:animate-none" />
        ))}
      </div>
    );
  } else if (q.isError) {
    body = (
      <Callout tone="warning" title="Couldn't read .env.example">
        {describeError(q.error, "The repository couldn't be read.")}
      </Callout>
    );
  } else if (q.data?.kind === "unavailable") {
    body = (
      <Callout tone="info" title="Import from .env.example isn't available on this server yet">
        Update kuso to import keys from your repo. Until then, paste the file with Add → Paste .env.
      </Callout>
    );
  } else if (example && !example.file) {
    body = (
      <Callout tone="neutral" title={`No example file on ${example.branch}`}>
        Checked{" "}
        {example.checked.map((p, i) => (
          <span key={p}>
            {i > 0 && (i === example.checked.length - 1 ? " and " : ", ")}
            <span className="font-mono text-xs">{p}</span>
          </span>
        ))}
        . Commit one of these to import its keys.
      </Callout>
    );
  } else if (example && rows.length === 0) {
    body = (
      <Callout tone="neutral" title={`${example.file} has no variables`}>
        No KEY=value lines were found in it.
      </Callout>
    );
  } else {
    body = (
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2 text-xs text-[var(--text-secondary)]">
          <span>
            {fresh.length} new {fresh.length === 1 ? "key" : "keys"}
            {rows.length > fresh.length && ` · ${rows.length - fresh.length} already set`}
          </span>
          {fresh.length > 1 && (
            <button
              type="button"
              onClick={() => setSelected(allOn ? new Set() : new Set(fresh.map((r) => r.key)))}
              className="rounded px-1.5 py-0.5 font-medium text-[var(--text-primary)] hover:bg-[var(--bg-tertiary)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--ring)]"
            >
              {allOn ? "Select none" : "Select all"}
            </button>
          )}
        </div>
        <ul className="max-h-[50vh] divide-y divide-[var(--border-subtle)] overflow-y-auto rounded-md border border-[var(--border-subtle)]">
          {rows.map((r) => {
            const secret = !!r.value && isSensitiveVar(r.key, r.value);
            const id = `envex-${r.key}`;
            return (
              <li key={r.key} className={cn("flex items-start gap-2.5 px-3 py-2", r.exists && "opacity-70")}>
                <input
                  id={id}
                  type="checkbox"
                  checked={!r.exists && selected.has(r.key)}
                  disabled={r.exists}
                  onChange={() => toggle(r.key)}
                  className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-[var(--accent)]"
                />
                <label htmlFor={id} className="min-w-0 flex-1 cursor-pointer">
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="truncate font-mono text-xs font-medium text-[var(--text-primary)]">{r.key}</span>
                    {r.exists && <VarBadge title="The service already has this key; importing never overwrites it.">already set · skipped</VarBadge>}
                    {!r.exists && secret && (
                      <VarBadge tone="warn" title="This example value looks like a real secret. Check it before saving.">
                        <Lock aria-hidden className="mr-0.5 inline h-3 w-3 align-[-2px]" />
                        looks secret
                      </VarBadge>
                    )}
                  </span>
                  <span className="mt-0.5 block truncate font-mono text-xs text-[var(--text-secondary)]">
                    {r.value === "" ? <span className="font-sans italic text-[var(--text-tertiary)]">no default</span> : secret ? "••••••••" : r.value}
                  </span>
                  {r.comment && <span className="mt-0.5 block truncate text-xs text-[var(--text-tertiary)]">{r.comment}</span>}
                </label>
              </li>
            );
          })}
        </ul>
        {example?.truncated && (
          <p className="text-xs text-[var(--text-tertiary)]">Only the first {rows.length} keys are shown.</p>
        )}
      </div>
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Import from {example?.file || ".env.example"}</DialogTitle>
          <DialogDescription>
            {example?.file
              ? `Read from the ${example.branch} branch. Imported keys are added as unsaved changes; existing keys are never changed.`
              : "Adds the keys from your repo's example file as unsaved changes. Existing keys are never changed."}
          </DialogDescription>
        </DialogHeader>
        {body}
        <DialogFooter>
          {fresh.length > 0 ? (
            <>
              <Button size="sm" variant="ghost" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button
                size="sm"
                disabled={picked.length === 0}
                onClick={() => {
                  onImport(picked);
                  onOpenChange(false);
                }}
              >
                {picked.length > 0 ? `Add ${picked.length} ${picked.length === 1 ? "variable" : "variables"}` : "Select keys to add"}
              </Button>
            </>
          ) : (
            <Button size="sm" variant="outline" onClick={() => onOpenChange(false)}>
              Close
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
