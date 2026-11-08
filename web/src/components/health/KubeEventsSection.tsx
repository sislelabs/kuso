"use client";

import { useState } from "react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorState } from "@/components/shared/QueryErrorState";
import { useCan, Perms } from "@/features/auth";
import { eventTime, useKubeEvents } from "@/features/kube-events";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

// KubeEventsSection is the web view of `kuso get events [--namespace]`:
// the newest 200 kube Events in one namespace. Admin-only server-side.
export function KubeEventsSection() {
  const isAdmin = useCan(Perms.SettingsAdmin);
  const [nsInput, setNsInput] = useState("");
  const [namespace, setNamespace] = useState("");
  const [warningsOnly, setWarningsOnly] = useState(false);
  const events = useKubeEvents(namespace, isAdmin);

  if (!isAdmin) return null;

  const rows = (events.data ?? []).filter((e) => !warningsOnly || e.type === "Warning");

  return (
    <section id="kube-events" className="mt-10 scroll-mt-4">
      <header className="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="font-heading text-lg font-semibold tracking-tight">Kubernetes events</h2>
          <p className="mt-0.5 text-xs text-[var(--text-secondary)]">
            Newest 200 events in{" "}
            <span className="font-mono">{namespace || "the kuso namespace"}</span>. Kube keeps events for about an hour.
          </p>
        </div>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setNamespace(nsInput.trim());
          }}
        >
          <Input
            value={nsInput}
            onChange={(e) => setNsInput(e.target.value)}
            placeholder="kuso"
            aria-label="Namespace"
            className="h-8 w-40 font-mono text-[13px]"
          />
          <Button type="submit" size="sm" variant="outline">
            Show
          </Button>
          <label className="flex cursor-pointer items-center gap-1.5 text-xs text-[var(--text-secondary)]">
            <input type="checkbox" checked={warningsOnly} onChange={(e) => setWarningsOnly(e.target.checked)} />
            Warnings only
          </label>
        </form>
      </header>

      {events.isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : events.isError ? (
        <QueryErrorState what="Kubernetes events" error={events.error} onRetry={() => void events.refetch()} />
      ) : rows.length === 0 ? (
        <p className="rounded-md border border-dashed border-[var(--border-subtle)] p-6 text-center text-sm text-[var(--text-tertiary)]">
          {warningsOnly ? "No warning events" : "No events"} in {namespace || "the kuso namespace"}.
        </p>
      ) : (
        <div className="overflow-x-auto rounded-md border border-[var(--border-subtle)]">
          <table className="w-full text-left text-xs">
            <thead className="border-b border-[var(--border-subtle)] bg-[var(--bg-secondary)] text-xs text-[var(--text-tertiary)]">
              <tr>
                <th className="px-3 py-2 font-medium">When</th>
                <th className="px-3 py-2 font-medium">Type</th>
                <th className="px-3 py-2 font-medium">Reason</th>
                <th className="px-3 py-2 font-medium">Object</th>
                <th className="px-3 py-2 font-medium">Message</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-[var(--border-subtle)]">
              {rows.map((e) => {
                const ts = eventTime(e);
                const obj = e.involvedObject;
                return (
                  <tr key={e.metadata.uid ?? e.metadata.name} className="align-top">
                    <td className="whitespace-nowrap px-3 py-1.5 text-[var(--text-tertiary)]" title={ts}>
                      {relativeTime(ts)}
                      {(e.count ?? 0) > 1 && <span className="ml-1">×{e.count}</span>}
                    </td>
                    <td className="px-3 py-1.5">
                      <span
                        className={cn(
                          "inline-flex rounded border px-1.5 py-px text-xs",
                          e.type === "Warning"
                            ? "border-[var(--warning)]/40 bg-[var(--warning-subtle)] text-[var(--warning)]"
                            : "border-transparent px-0 text-[var(--text-tertiary)]",
                        )}
                      >
                        {e.type ?? "—"}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 text-[var(--text-primary)]">{e.reason ?? "—"}</td>
                    <td className="max-w-[14rem] truncate px-3 py-1.5 font-mono text-[var(--text-secondary)]" title={obj?.name}>
                      {obj ? `${obj.kind ?? ""}/${obj.name ?? ""}` : "—"}
                    </td>
                    <td className="min-w-[16rem] px-3 py-1.5 text-[var(--text-secondary)] [overflow-wrap:anywhere]">{e.message}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
