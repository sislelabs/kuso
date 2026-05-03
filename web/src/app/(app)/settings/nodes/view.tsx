"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { motion, AnimatePresence } from "motion/react";
import { api } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Server, Plus, X, Save, MapPin, Tag } from "lucide-react";
// X is already imported above; the modal close button reuses it.
import { cn } from "@/lib/utils";
import type { NodeSummary } from "@/components/layout/ServersPopover";

interface Label {
  key: string;
  value: string;
}

// Common label keys — used as quick-add suggestions in the inline
// editor. Order = popularity. Adding a `region` chip is special on
// the server side (it gets a matching NoSchedule taint) so we surface
// it first + with a marker icon on the chip.
const SUGGESTED_KEYS = ["region", "tier", "gpu", "arch", "instance-type", "zone"] as const;

// All labels we manage — Map<nodeName, Label[]> — keyed off the
// initial server response so we can diff per-card without each
// NodeCard owning its own dirty state.
type NodeLabels = Record<string, Label[]>;

function fromNodes(nodes: NodeSummary[]): NodeLabels {
  const out: NodeLabels = {};
  for (const n of nodes) {
    out[n.name] = Object.entries(n.kusoLabels ?? {}).map(([k, v]) => ({ key: k, value: v }));
  }
  return out;
}

// Stable serialization for diffing. Sort by key so a re-order of
// labels (which doesn't matter to k8s) doesn't show as dirty.
function serialize(labels: Label[]): string {
  return JSON.stringify(
    [...labels]
      .filter((l) => l.key.trim() !== "")
      .sort((a, b) => a.key.localeCompare(b.key))
      .map((l) => [l.key.trim(), l.value])
  );
}

export function NodesView() {
  const qc = useQueryClient();
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
  });

  // Hoist edits up here so we can render a single floating save bar
  // covering every node's pending changes — same pattern as service
  // settings. Per-node Save buttons hidden in tiny card headers were
  // the original DX bug.
  const [edits, setEdits] = useState<NodeLabels>({});
  const baseline = useMemo(() => fromNodes(nodes.data ?? []), [nodes.data]);
  const baselineRef = useRef(baseline);

  // Re-baseline when the server data changes AND the user has no
  // pending edits. Otherwise typing would get clobbered every
  // refetch.
  useEffect(() => {
    const dirtyAny = Object.keys(edits).length > 0;
    if (!dirtyAny) {
      baselineRef.current = baseline;
      setEdits({});
    } else {
      baselineRef.current = baseline;
    }
  }, [baseline]); // eslint-disable-line react-hooks/exhaustive-deps

  // The server endpoint takes ONE node at a time. We fan out a Save
  // by submitting a mutation per dirty node and tracking aggregate
  // pending state.
  const [saving, setSaving] = useState(false);
  const saveAll = async () => {
    setSaving(true);
    try {
      for (const [nodeName, labels] of Object.entries(edits)) {
        const body = {
          labels: Object.fromEntries(
            labels.filter((l) => l.key.trim()).map((l) => [l.key.trim(), l.value])
          ),
        };
        await api(`/api/kubernetes/nodes/${encodeURIComponent(nodeName)}/labels`, {
          method: "PUT",
          body,
        });
      }
      toast.success(
        Object.keys(edits).length === 1
          ? "Node updated"
          : `${Object.keys(edits).length} nodes updated`
      );
      setEdits({});
      await qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save");
    } finally {
      setSaving(false);
    }
  };

  const dirtyCount = Object.keys(edits).length;

  // Each NodeCard reads from edits[node] when present, else baseline.
  const labelsFor = (n: NodeSummary): Label[] =>
    edits[n.name] ?? baseline[n.name] ?? [];
  const setLabels = (n: NodeSummary, next: Label[]) => {
    const baseSer = serialize(baseline[n.name] ?? []);
    const nextSer = serialize(next);
    setEdits((cur) => {
      const copy = { ...cur };
      if (nextSer === baseSer) {
        delete copy[n.name];
      } else {
        copy[n.name] = next;
      }
      return copy;
    });
  };

  return (
    <div className="mx-auto max-w-4xl p-6 lg:p-8 pb-24">
      <header className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="font-heading text-2xl font-semibold tracking-tight">Cluster nodes</h1>
          <p className="mt-1 text-sm text-[var(--text-secondary)]">
            Tag nodes with labels (e.g. <span className="font-mono">region=eu</span>,{" "}
            <span className="font-mono">tier=premium</span>) and projects can pin to them.
            Bare keys without a value work too — useful for capability flags like{" "}
            <span className="font-mono">gpu</span>.
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
            <NodeCard
              key={n.name}
              node={n}
              labels={labelsFor(n)}
              onChange={(next) => setLabels(n, next)}
              isDirty={n.name in edits}
            />
          ))}
        </ul>
      )}

      {/* Floating save bar — appears the moment any node is dirty.
          Covers every dirty node in one click. Same shape as the
          service settings panel for consistency. */}
      <FloatingSaveBar
        dirty={dirtyCount > 0}
        pending={saving}
        count={dirtyCount}
        onSave={saveAll}
        onReset={() => setEdits({})}
      />
    </div>
  );
}

function FloatingSaveBar({
  dirty,
  pending,
  count,
  onSave,
  onReset,
}: {
  dirty: boolean;
  pending: boolean;
  count: number;
  onSave: () => void;
  onReset: () => void;
}) {
  return (
    <AnimatePresence>
      {dirty && (
        <motion.div
          initial={{ y: 60, opacity: 0 }}
          animate={{ y: 0, opacity: 1 }}
          exit={{ y: 60, opacity: 0 }}
          transition={{ type: "spring", stiffness: 360, damping: 32 }}
          className="fixed bottom-4 right-4 z-30 flex items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-2 shadow-[var(--shadow-lg)]"
        >
          <span className="mr-auto inline-flex items-center gap-1.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            <span className="inline-block h-1.5 w-1.5 rounded-full bg-amber-400" />
            unsaved on {count} {count === 1 ? "node" : "nodes"}
          </span>
          <Button size="sm" variant="outline" onClick={onReset} disabled={pending}>
            Discard
          </Button>
          <Button size="sm" onClick={onSave} disabled={pending}>
            <Save className="h-3 w-3" />
            {pending ? "Saving…" : "Save changes"}
          </Button>
        </motion.div>
      )}
    </AnimatePresence>
  );
}

function NodeCard({
  node,
  labels,
  onChange,
  isDirty,
}: {
  node: NodeSummary;
  labels: Label[];
  onChange: (next: Label[]) => void;
  isDirty: boolean;
}) {
  return (
    <li
      className={cn(
        "rounded-md border bg-[var(--bg-secondary)] p-4 transition-colors",
        isDirty
          ? "border-amber-500/30 bg-amber-500/[0.02]"
          : "border-[var(--border-subtle)]"
      )}
    >
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
          <div className="mt-1 flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-[var(--text-tertiary)]">
            {node.roles.map((r) => (
              <span key={r} className="rounded bg-[var(--bg-tertiary)] px-1.5 py-0.5">
                {r}
              </span>
            ))}
            {node.zone && <span>zone {node.zone}</span>}
            {!node.schedulable && (
              <span className="rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-400">cordoned</span>
            )}
            {isDirty && (
              <span className="ml-auto rounded bg-amber-500/10 px-1.5 py-0.5 text-amber-400">
                unsaved
              </span>
            )}
          </div>
        </div>
      </header>

      <NodeStats node={node} />

      {/* The chip strip is the entire label UI now. No header label,
          no separate "Labels" section — the chips ARE the labels.
          Adding/removing happens here too. */}
      <ChipStrip labels={labels} onChange={onChange} />
    </li>
  );
}

function ChipStrip({
  labels,
  onChange,
}: {
  labels: Label[];
  onChange: (next: Label[]) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [draftKey, setDraftKey] = useState("");
  const [draftValue, setDraftValue] = useState("");
  const keyRef = useRef<HTMLInputElement>(null);

  // Focus the key field automatically when the editor opens.
  useEffect(() => {
    if (adding) keyRef.current?.focus();
  }, [adding]);

  const commit = () => {
    const key = draftKey.trim();
    if (!key) {
      // Empty key = discard the in-progress draft. Quieter than
      // toasting an error for a no-op.
      setAdding(false);
      setDraftKey("");
      setDraftValue("");
      return;
    }
    if (!/^[a-z0-9](?:[a-z0-9_-]{0,61}[a-z0-9])?$/.test(key)) {
      toast.error(`"${key}" — keys must be lowercase, dashes/underscores, ≤63 chars`);
      return;
    }
    if (labels.some((l) => l.key === key)) {
      toast.error(`Label "${key}" already exists on this node`);
      return;
    }
    onChange([...labels, { key, value: draftValue.trim() }]);
    // Stay in adding mode but reset for fast multi-add.
    setDraftKey("");
    setDraftValue("");
    keyRef.current?.focus();
  };

  const cancel = () => {
    setAdding(false);
    setDraftKey("");
    setDraftValue("");
  };

  const remove = (i: number) => {
    onChange(labels.filter((_, j) => j !== i));
  };

  return (
    <div className="mt-3 flex flex-wrap items-center gap-1.5">
      {labels.map((l, i) => (
        <Chip key={`${l.key}-${i}`} label={l} onRemove={() => remove(i)} />
      ))}

      {!adding ? (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-dashed border-[var(--border-subtle)] px-2 font-mono text-[11px] text-[var(--text-tertiary)] hover:border-[var(--accent)]/40 hover:bg-[var(--accent-subtle)] hover:text-[var(--accent)]"
        >
          <Plus className="h-3 w-3" />
          {labels.length === 0 ? "Add label" : "Add"}
        </button>
      ) : (
        <DraftEditor
          keyRef={keyRef}
          draftKey={draftKey}
          draftValue={draftValue}
          existingKeys={labels.map((l) => l.key)}
          onChangeKey={setDraftKey}
          onChangeValue={setDraftValue}
          onCommit={commit}
          onCancel={cancel}
        />
      )}
    </div>
  );
}

function Chip({ label, onRemove }: { label: Label; onRemove: () => void }) {
  // region is special — server-side it auto-applies a matching
  // NoSchedule taint. Surface that with the MapPin icon so users
  // know this chip means more than "metadata."
  const isRegion = label.key === "region";
  return (
    <span
      className={cn(
        "group inline-flex h-7 items-center gap-1.5 rounded-md border px-2 font-mono text-[11px]",
        isRegion
          ? "border-[var(--accent)]/30 bg-[var(--accent-subtle)] text-[var(--accent)]"
          : "border-[var(--border-subtle)] bg-[var(--bg-tertiary)]/60 text-[var(--text-secondary)]"
      )}
    >
      {isRegion ? <MapPin className="h-3 w-3" /> : <Tag className="h-3 w-3 opacity-60" />}
      <span className="font-medium">{label.key}</span>
      {label.value && (
        <>
          <span className="text-[var(--text-tertiary)]/60">=</span>
          <span>{label.value}</span>
        </>
      )}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove ${label.key}`}
        className="ml-0.5 inline-flex h-4 w-4 items-center justify-center rounded text-[var(--text-tertiary)] opacity-0 transition-opacity hover:bg-[var(--bg-primary)] hover:text-red-400 group-hover:opacity-100"
      >
        <X className="h-2.5 w-2.5" />
      </button>
    </span>
  );
}

function DraftEditor({
  keyRef,
  draftKey,
  draftValue,
  existingKeys,
  onChangeKey,
  onChangeValue,
  onCommit,
  onCancel,
}: {
  keyRef: React.RefObject<HTMLInputElement | null>;
  draftKey: string;
  draftValue: string;
  existingKeys: string[];
  onChangeKey: (v: string) => void;
  onChangeValue: (v: string) => void;
  onCommit: () => void;
  onCancel: () => void;
}) {
  const valueRef = useRef<HTMLInputElement>(null);
  // Filter the suggestions to keys not already on this node.
  const suggestions = SUGGESTED_KEYS.filter(
    (s) => !existingKeys.includes(s) && (draftKey === "" || s.startsWith(draftKey.toLowerCase()))
  );

  const onKeyKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      // Tab-like behaviour: Enter on key field → focus value (so
      // "region" + Enter + "eu" + Enter works as a fast keyboard
      // flow). Empty key + Enter cancels.
      if (draftKey.trim()) valueRef.current?.focus();
      else onCancel();
    } else if (e.key === "Escape") {
      onCancel();
    } else if (e.key === "Tab" && !e.shiftKey && draftKey.trim() === "") {
      e.preventDefault();
      onCancel();
    }
  };
  const onValueKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      onCommit();
    } else if (e.key === "Escape") {
      onCancel();
    }
  };

  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-[var(--accent)]/30 bg-[var(--accent-subtle)]/40 px-1 py-0.5">
      <Input
        ref={keyRef}
        value={draftKey}
        onChange={(e) => onChangeKey(e.target.value)}
        onKeyDown={onKeyKeyDown}
        placeholder="key"
        list="kuso-node-label-suggestions"
        className="h-6 w-24 border-0 bg-transparent font-mono text-[11px] focus-visible:ring-0"
      />
      <span className="font-mono text-xs text-[var(--text-tertiary)]/60">=</span>
      <Input
        ref={valueRef}
        value={draftValue}
        onChange={(e) => onChangeValue(e.target.value)}
        onKeyDown={onValueKeyDown}
        placeholder="value"
        className="h-6 w-24 border-0 bg-transparent font-mono text-[11px] focus-visible:ring-0"
      />
      <button
        type="button"
        onClick={onCommit}
        className="inline-flex h-5 w-5 items-center justify-center rounded text-[var(--accent)] hover:bg-[var(--bg-primary)]/50"
        title="Add (Enter)"
        aria-label="Add label"
      >
        <Plus className="h-3 w-3" />
      </button>
      <button
        type="button"
        onClick={onCancel}
        className="inline-flex h-5 w-5 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)]/50 hover:text-red-400"
        title="Cancel (Esc)"
        aria-label="Cancel"
      >
        <X className="h-3 w-3" />
      </button>
      {suggestions.length > 0 && (
        <span className="ml-1 inline-flex items-center gap-0.5">
          {suggestions.slice(0, 3).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => {
                onChangeKey(s);
                valueRef.current?.focus();
              }}
              className="inline-flex h-5 items-center rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)]/60 px-1.5 font-mono text-[10px] text-[var(--text-tertiary)] hover:border-[var(--accent)]/30 hover:text-[var(--accent)]"
            >
              {s}
            </button>
          ))}
        </span>
      )}
    </span>
  );
}

// NodeStats renders the live capacity + usage row. cpu in milli-cores
// (1000 = 1 core), memory + disk in bytes. metrics-server is optional
// — when usage is unavailable we show "—" rather than 0% which would
// be misleading.
//
// Each tile is clickable: it opens a drill-down with 7 days of
// history (sampled every 30 min server-side) so the operator can
// see trends instead of a single point-in-time number. The "metric"
// param tells the modal which row to highlight.
function NodeStats({ node }: { node: NodeSummary }) {
  const [openMetric, setOpenMetric] = useState<NodeMetricKind | null>(null);
  const hasUsage = (node.cpuUsageMilli ?? 0) > 0 || (node.memUsageBytes ?? 0) > 0;
  return (
    <>
      <div className="mt-3 grid grid-cols-3 gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 text-[10px]">
        <Stat
          label="CPU"
          value={
            hasUsage
              ? `${formatCPU(node.cpuUsageMilli ?? 0)} / ${formatCPU(node.cpuCapacityMilli ?? 0)}`
              : `cap ${formatCPU(node.cpuCapacityMilli ?? 0)}`
          }
          pct={
            hasUsage && node.cpuCapacityMilli
              ? Math.min(100, Math.round(((node.cpuUsageMilli ?? 0) / node.cpuCapacityMilli) * 100))
              : null
          }
          onClick={() => setOpenMetric("cpu")}
        />
        <Stat
          label="RAM"
          value={
            hasUsage
              ? `${formatBytes(node.memUsageBytes ?? 0)} / ${formatBytes(node.memCapacityBytes ?? 0)}`
              : `cap ${formatBytes(node.memCapacityBytes ?? 0)}`
          }
          pct={
            hasUsage && node.memCapacityBytes
              ? Math.min(100, Math.round(((node.memUsageBytes ?? 0) / node.memCapacityBytes) * 100))
              : null
          }
          onClick={() => setOpenMetric("mem")}
        />
        <Stat
          label="Disk"
          value={
            node.diskCapacityBytes
              ? `${formatBytes((node.diskCapacityBytes ?? 0) - (node.diskAvailableBytes ?? 0))} / ${formatBytes(node.diskCapacityBytes ?? 0)}`
              : "—"
          }
          pct={
            node.diskCapacityBytes && node.diskAvailableBytes !== undefined
              ? Math.min(
                  100,
                  Math.round(
                    ((node.diskCapacityBytes - node.diskAvailableBytes) / node.diskCapacityBytes) * 100
                  )
                )
              : null
          }
          onClick={() => setOpenMetric("disk")}
        />
        {node.podsCapacity ? (
          <p className="col-span-3 mt-1 font-mono text-[10px] text-[var(--text-tertiary)]">
            {node.pods ?? 0} / {node.podsCapacity} pods scheduled
          </p>
        ) : null}
      </div>
      {openMetric && (
        <NodeHistoryModal
          node={node}
          metric={openMetric}
          onClose={() => setOpenMetric(null)}
        />
      )}
    </>
  );
}

type NodeMetricKind = "cpu" | "mem" | "disk";

function Stat({
  label,
  value,
  pct,
  onClick,
}: {
  label: string;
  value: string;
  pct: number | null;
  onClick?: () => void;
}) {
  // Color the bar by pressure level — green <60%, amber 60-85, red >85.
  const bar =
    pct === null
      ? "bg-[var(--bg-tertiary)]"
      : pct < 60
        ? "bg-emerald-500"
        : pct < 85
          ? "bg-amber-500"
          : "bg-red-500";
  return (
    <button
      type="button"
      onClick={onClick}
      className="space-y-1 rounded p-1 text-left transition-colors hover:bg-[var(--bg-tertiary)]/50 focus:outline-none focus-visible:ring-1 focus-visible:ring-[var(--accent)]"
    >
      <div className="flex items-center justify-between font-mono text-[var(--text-tertiary)]">
        <span>{label}</span>
        {pct !== null && <span className="text-[var(--text-secondary)]">{pct}%</span>}
      </div>
      <div className="h-1 w-full overflow-hidden rounded bg-[var(--bg-tertiary)]">
        <div
          className={cn("h-full transition-all", bar)}
          style={{ width: pct === null ? "0%" : `${pct}%` }}
        />
      </div>
      <div className="font-mono text-[var(--text-secondary)]">{value}</div>
    </button>
  );
}

// NodeHistoryModal renders 7 days of samples for a single metric.
// Backed by /api/kubernetes/nodes/<name>/history which reads the
// SQLite NodeMetric table. The chart is a hand-rolled SVG path —
// recharts/visx would add ~100KB to the bundle for one sparkline
// shape and we only need the line.
interface HistorySample {
  ts: string;
  cpuUsedMilli: number;
  cpuCapacityMilli: number;
  memUsedBytes: number;
  memCapacityBytes: number;
  diskAvailBytes: number;
  diskCapacityBytes: number;
}
interface HistoryResponse {
  node: string;
  samples: HistorySample[] | null;
}

function NodeHistoryModal({
  node,
  metric,
  onClose,
}: {
  node: NodeSummary;
  metric: NodeMetricKind;
  onClose: () => void;
}) {
  const history = useQuery({
    queryKey: ["kubernetes", "nodes", node.name, "history"],
    queryFn: () =>
      api<HistoryResponse>(`/api/kubernetes/nodes/${encodeURIComponent(node.name)}/history`),
    staleTime: 60_000,
  });

  // Esc to close — same affordance as every other overlay in the app.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const samples = history.data?.samples ?? [];
  const title = metric === "cpu" ? "CPU" : metric === "mem" ? "RAM" : "Disk";

  return (
    <div
      role="dialog"
      aria-modal="true"
      onClick={onClose}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-6"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-2xl rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] shadow-[var(--shadow-lg)]"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <div>
            <h2 className="font-mono text-sm font-medium">{node.name}</h2>
            <p className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              {title} · last 7 days · sampled every 30 min
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close"
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]"
          >
            <X className="h-4 w-4" />
          </button>
        </header>
        <div className="p-4">
          {history.isPending ? (
            <Skeleton className="h-40 w-full" />
          ) : history.isError ? (
            <p className="font-mono text-[11px] text-red-400">
              Failed to load history: {history.error?.message}
            </p>
          ) : samples.length === 0 ? (
            <p className="font-mono text-[11px] text-[var(--text-tertiary)]">
              No samples yet. The kuso server samples nodes every 30 min — check back shortly after
              a fresh deploy.
            </p>
          ) : (
            <Sparkline metric={metric} samples={samples} />
          )}
        </div>
      </div>
    </div>
  );
}

// Sparkline — width 100%, height 160. Renders the metric as a filled
// area + line on top so it reads at a glance even with a low sample
// count. Y axis is %used (always 0-100) so all three metrics are
// visually comparable.
function Sparkline({
  metric,
  samples,
}: {
  metric: NodeMetricKind;
  samples: HistorySample[];
}) {
  const W = 720;
  const H = 160;
  const pad = { left: 36, right: 12, top: 12, bottom: 24 };
  const innerW = W - pad.left - pad.right;
  const innerH = H - pad.top - pad.bottom;

  const points = samples.map((s) => {
    let pct = 0;
    let used = 0;
    let cap = 0;
    if (metric === "cpu") {
      used = s.cpuUsedMilli;
      cap = s.cpuCapacityMilli;
    } else if (metric === "mem") {
      used = s.memUsedBytes;
      cap = s.memCapacityBytes;
    } else {
      // Disk: capacity − available = used.
      cap = s.diskCapacityBytes;
      used = cap - s.diskAvailBytes;
    }
    if (cap > 0) pct = Math.min(100, (used / cap) * 100);
    return { ts: new Date(s.ts).getTime(), pct, used, cap };
  });

  const tMin = points[0]?.ts ?? 0;
  const tMax = points[points.length - 1]?.ts ?? tMin + 1;
  const tSpan = Math.max(1, tMax - tMin);

  const x = (t: number) => pad.left + ((t - tMin) / tSpan) * innerW;
  const y = (pct: number) => pad.top + (1 - pct / 100) * innerH;

  const linePath = points
    .map((p, i) => `${i === 0 ? "M" : "L"} ${x(p.ts).toFixed(1)} ${y(p.pct).toFixed(1)}`)
    .join(" ");
  const areaPath =
    points.length > 0
      ? `${linePath} L ${x(points[points.length - 1].ts).toFixed(1)} ${(pad.top + innerH).toFixed(1)} L ${x(points[0].ts).toFixed(1)} ${(pad.top + innerH).toFixed(1)} Z`
      : "";

  // Grid: 0/25/50/75/100 horizontal lines.
  const ticks = [0, 25, 50, 75, 100];

  // Color the line by the LATEST value's pressure tier so the chart
  // matches the inline tile's bar color.
  const latestPct = points[points.length - 1]?.pct ?? 0;
  const stroke =
    latestPct < 60 ? "rgb(16 185 129)" : latestPct < 85 ? "rgb(245 158 11)" : "rgb(239 68 68)";

  // X-axis labels: first + middle + last.
  const fmtTs = (ms: number) => {
    const d = new Date(ms);
    return `${d.getUTCMonth() + 1}/${d.getUTCDate()} ${String(d.getUTCHours()).padStart(2, "0")}:${String(d.getUTCMinutes()).padStart(2, "0")}`;
  };
  const xLabels = points.length === 0 ? [] : [
    points[0],
    points[Math.floor(points.length / 2)],
    points[points.length - 1],
  ];

  const latest = points[points.length - 1];
  const latestUsed =
    metric === "cpu"
      ? formatCPU(latest?.used ?? 0)
      : formatBytes(latest?.used ?? 0);
  const latestCap =
    metric === "cpu"
      ? formatCPU(latest?.cap ?? 0)
      : formatBytes(latest?.cap ?? 0);

  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between font-mono text-[11px]">
        <span className="text-[var(--text-tertiary)]">latest</span>
        <span>
          <span className="text-[var(--text-primary)]">{latestUsed}</span>{" "}
          <span className="text-[var(--text-tertiary)]">
            / {latestCap} ({latest ? Math.round(latest.pct) : 0}%)
          </span>
        </span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label={`${metric} history`}>
        {ticks.map((t) => (
          <g key={t}>
            <line
              x1={pad.left}
              x2={W - pad.right}
              y1={y(t)}
              y2={y(t)}
              stroke="var(--border-subtle)"
              strokeDasharray="2 2"
              strokeWidth={1}
            />
            <text
              x={pad.left - 6}
              y={y(t)}
              dy="0.32em"
              textAnchor="end"
              className="fill-[var(--text-tertiary)] font-mono text-[9px]"
            >
              {t}%
            </text>
          </g>
        ))}
        {areaPath && (
          <path d={areaPath} fill={stroke} opacity={0.12} />
        )}
        {linePath && (
          <path d={linePath} fill="none" stroke={stroke} strokeWidth={1.5} strokeLinejoin="round" />
        )}
        {xLabels.map((p, i) => (
          <text
            key={`${p.ts}-${i}`}
            x={x(p.ts)}
            y={H - 6}
            textAnchor={i === 0 ? "start" : i === xLabels.length - 1 ? "end" : "middle"}
            className="fill-[var(--text-tertiary)] font-mono text-[9px]"
          >
            {fmtTs(p.ts)}
          </text>
        ))}
      </svg>
      <p className="mt-2 font-mono text-[10px] text-[var(--text-tertiary)]">
        {points.length} sample{points.length === 1 ? "" : "s"}
      </p>
    </div>
  );
}

// formatCPU turns milli-CPU into a human string. 1000m → "1.0",
// 250m → "250m", 1500m → "1.5".
function formatCPU(milli: number): string {
  if (milli === 0) return "0";
  if (milli < 1000) return `${milli}m`;
  const cores = milli / 1000;
  return cores >= 10 ? `${Math.round(cores)}` : cores.toFixed(1);
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return "0";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v >= 100 || i === 0 ? `${Math.round(v)}${units[i]}` : `${v.toFixed(1)}${units[i]}`;
}
