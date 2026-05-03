"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Server, Plus, X, Save } from "lucide-react";
import { cn } from "@/lib/utils";
import type { NodeSummary } from "@/components/layout/ServersPopover";

interface LabelRow {
  key: string;
  value: string;
}

// NodesView is the long-form node manager. We expose a single concept
// to the operator: kuso labels (key=value chips). The server
// translates conventions ("region" → matching NoSchedule taint) so the
// user never sees taints/tolerations directly. That's the whole point
// of the abstraction — labels are familiar, taints aren't.
export function NodesView() {
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
  });

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8">
      <header className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster nodes</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Tag nodes with labels (e.g.{" "}
            <span className="font-mono">region=eu</span>,{" "}
            <span className="font-mono">tier=premium</span>) and projects can pin to them.
            kuso translates the conventions into the right kube primitives behind the scenes.
          </p>
        </div>
        <Server className="h-6 w-6 shrink-0 text-[var(--text-tertiary)]" />
      </header>

      {nodes.isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : nodes.isError ? (
        <p className="rounded-md border border-red-500/30 bg-red-500/5 p-4 text-sm text-red-400">
          Failed to load nodes: {nodes.error?.message}
        </p>
      ) : (
        <ul className="space-y-3">
          {(nodes.data ?? []).map((n) => (
            <NodeCard key={n.name} node={n} />
          ))}
        </ul>
      )}
    </div>
  );
}

function NodeCard({ node }: { node: NodeSummary }) {
  const qc = useQueryClient();
  const initialRows: LabelRow[] = Object.entries(node.kusoLabels ?? {}).map(([k, v]) => ({
    key: k,
    value: v,
  }));
  const [rows, setRows] = useState<LabelRow[]>(initialRows);

  const dirty =
    JSON.stringify(rows.filter((r) => r.key.trim()).map((r) => [r.key, r.value]).sort()) !==
    JSON.stringify(Object.entries(node.kusoLabels ?? {}).sort());

  const save = useMutation({
    mutationFn: (next: LabelRow[]) =>
      api(`/api/kubernetes/nodes/${encodeURIComponent(node.name)}/labels`, {
        method: "PUT",
        body: {
          labels: Object.fromEntries(next.filter((r) => r.key.trim()).map((r) => [r.key.trim(), r.value])),
        },
      }),
    onSuccess: () => {
      toast.success(`${node.name} updated`);
      qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });

  return (
    <li className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-4">
      <header className="flex items-start gap-3">
        <span
          className={cn(
            "mt-1 inline-block h-2 w-2 shrink-0 rounded-full",
            node.ready ? "bg-emerald-400" : "bg-red-400"
          )}
          title={node.ready ? "Ready" : "NotReady"}
        />
        <div className="min-w-0 flex-1">
          <h3 className="truncate font-mono text-sm font-medium">{node.name}</h3>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] font-mono text-[var(--text-tertiary)]">
            {node.roles.map((r) => (
              <span key={r} className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5">
                {r}
              </span>
            ))}
            {node.zone && <span>zone {node.zone}</span>}
            {!node.schedulable && (
              <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-400">cordoned</span>
            )}
          </div>
        </div>
        <Button size="sm" onClick={() => save.mutate(rows)} disabled={!dirty || save.isPending}>
          <Save className="h-3 w-3" />
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </header>

      <div className="mt-4">
        <div className="mb-2 flex items-center justify-between">
          <h4 className="text-xs font-medium text-[var(--text-secondary)]">Labels</h4>
          <button
            type="button"
            onClick={() => setRows((r) => [...r, { key: "", value: "" }])}
            className="inline-flex items-center gap-1 text-[10px] text-[var(--accent)] hover:underline"
          >
            <Plus className="h-3 w-3" /> add label
          </button>
        </div>

        {rows.length === 0 ? (
          <p className="rounded-md border border-dashed border-[var(--border-subtle)] px-3 py-4 text-center text-[10px] text-[var(--text-tertiary)]">
            No labels. Add <span className="font-mono">region=eu</span> to make this node available
            to projects pinned to <span className="font-mono">eu</span>.
          </p>
        ) : (
          <ul className="space-y-1.5">
            {rows.map((row, i) => (
              <li key={i} className="flex items-center gap-1.5">
                <Input
                  value={row.key}
                  onChange={(e) =>
                    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                  }
                  placeholder="key"
                  className="h-7 w-32 font-mono text-[11px]"
                />
                <span className="font-mono text-xs text-[var(--text-tertiary)]">=</span>
                <Input
                  value={row.value}
                  onChange={(e) =>
                    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))
                  }
                  placeholder="value"
                  className="h-7 flex-1 font-mono text-[11px]"
                />
                <button
                  type="button"
                  onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
                  aria-label="Remove label"
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
                >
                  <X className="h-3 w-3" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <p className="mt-2 text-[10px] text-[var(--text-tertiary)]">
          Tip: <span className="font-mono">region</span> is special — kuso also adds a matching
          taint so only projects pinned to that region land on this node.
        </p>
      </div>
    </li>
  );
}
