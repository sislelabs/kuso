"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api-client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Server, Plus, Trash2, Save } from "lucide-react";
import { cn } from "@/lib/utils";
import type { NodeSummary } from "@/components/layout/ServersPopover";

const TAINT_EFFECTS = ["NoSchedule", "PreferNoSchedule", "NoExecute"] as const;
type Effect = (typeof TAINT_EFFECTS)[number];

interface TaintRow {
  key: string;
  value: string;
  effect: Effect;
}

// NodesView is the long-form editor for cluster nodes. Each node card
// surfaces region/zone, role badges, and a mutable taint list.
// Saving a row PATCHes /api/kubernetes/nodes/<name>/taints with the
// new list (replace, not merge — the server uses $retainKeys to
// guarantee that semantic). The same card has a small label editor so
// an operator can drop kuso.sislelabs.com/region=eu without leaving
// the page.
export function NodesView() {
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
  });

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8">
      <header className="mb-6 flex items-center justify-between">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster nodes</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Group nodes into regions, then taint them so a project can pin to
            <span className="font-mono"> region=eu</span> or
            <span className="font-mono"> region=na</span>.
          </p>
        </div>
        <Server className="h-6 w-6 text-[var(--text-tertiary)]" />
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
  const [taints, setTaints] = useState<TaintRow[]>(
    node.taints.map((t) => ({ key: t.key, value: t.value ?? "", effect: (t.effect as Effect) || "NoSchedule" }))
  );
  const [regionDraft, setRegionDraft] = useState(
    node.region || node.labels["kuso.sislelabs.com/region"] || ""
  );

  const dirty =
    JSON.stringify(taints.map((t) => ({ ...t, value: t.value || "" }))) !==
      JSON.stringify(node.taints.map((t) => ({ key: t.key, value: t.value ?? "", effect: t.effect as Effect }))) ||
    regionDraft !== (node.region || node.labels["kuso.sislelabs.com/region"] || "");

  const saveTaints = useMutation({
    mutationFn: (rows: TaintRow[]) =>
      api(`/api/kubernetes/nodes/${encodeURIComponent(node.name)}/taints`, {
        method: "PATCH",
        body: { taints: rows.map((r) => ({ key: r.key, value: r.value || undefined, effect: r.effect })) },
      }),
  });

  const saveLabels = useMutation({
    mutationFn: (region: string) =>
      api(`/api/kubernetes/nodes/${encodeURIComponent(node.name)}/labels`, {
        method: "PATCH",
        // Empty string = delete the label, matching `kubectl label foo-`.
        body: { labels: { "kuso.sislelabs.com/region": region } },
      }),
  });

  const onSave = async () => {
    const cleaned = taints.filter((t) => t.key.trim() !== "");
    try {
      if (
        regionDraft !== (node.region || node.labels["kuso.sislelabs.com/region"] || "")
      ) {
        await saveLabels.mutateAsync(regionDraft);
      }
      await saveTaints.mutateAsync(cleaned);
      toast.success(`${node.name} updated`);
      qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save node");
    }
  };

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
        <Button
          size="sm"
          onClick={onSave}
          disabled={!dirty || saveTaints.isPending || saveLabels.isPending}
        >
          <Save className="h-3 w-3" />
          {saveTaints.isPending || saveLabels.isPending ? "Saving…" : "Save"}
        </Button>
      </header>

      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor={`region-${node.name}`} className="text-xs">
            kuso region
          </Label>
          <Input
            id={`region-${node.name}`}
            value={regionDraft}
            onChange={(e) => setRegionDraft(e.target.value)}
            placeholder="e.g. eu, na, ap-1"
            className="h-8 font-mono text-xs"
          />
          <p className="text-[10px] text-[var(--text-tertiary)]">
            Stored as <span className="font-mono">kuso.sislelabs.com/region=&lt;value&gt;</span>.
          </p>
        </div>

        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <Label className="text-xs">Taints</Label>
            <button
              type="button"
              onClick={() => setTaints((rows) => [...rows, { key: "", value: "", effect: "NoSchedule" }])}
              className="inline-flex items-center gap-1 text-[10px] text-[var(--accent)] hover:underline"
            >
              <Plus className="h-3 w-3" />
              add
            </button>
          </div>
          {taints.length === 0 ? (
            <p className="text-[10px] text-[var(--text-tertiary)]">
              No taints. Workloads land here freely.
            </p>
          ) : (
            <ul className="space-y-1.5">
              {taints.map((t, i) => (
                <li key={i} className="flex items-center gap-1.5">
                  <Input
                    value={t.key}
                    onChange={(e) =>
                      setTaints((rows) => rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                    }
                    placeholder="key"
                    className="h-7 flex-1 font-mono text-[11px]"
                  />
                  <Input
                    value={t.value}
                    onChange={(e) =>
                      setTaints((rows) => rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))
                    }
                    placeholder="value"
                    className="h-7 w-24 font-mono text-[11px]"
                  />
                  <select
                    value={t.effect}
                    onChange={(e) =>
                      setTaints((rows) =>
                        rows.map((r, j) => (j === i ? { ...r, effect: e.target.value as Effect } : r))
                      )
                    }
                    className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-1.5 font-mono text-[10px]"
                  >
                    {TAINT_EFFECTS.map((eff) => (
                      <option key={eff} value={eff}>
                        {eff}
                      </option>
                    ))}
                  </select>
                  <button
                    type="button"
                    onClick={() => setTaints((rows) => rows.filter((_, j) => j !== i))}
                    aria-label="Remove taint"
                    className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
                  >
                    <Trash2 className="h-3 w-3" />
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </li>
  );
}
