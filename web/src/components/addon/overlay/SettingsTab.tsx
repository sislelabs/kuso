"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import {
  deleteAddon,
  setAddonPlacement,
  updateAddon,
  type UpdateAddonBody,
} from "@/features/projects";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { Settings, MapPin, Plus, X, Trash2 } from "lucide-react";
import type { NodeSummary } from "@/components/layout/ServersPopover";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

export function SettingsTab({
  project,
  addon,
  cr,
  onClose,
}: {
  project: string;
  addon: string;
  cr?: import("@/types/projects").KusoAddon;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [text, setText] = useState("");
  const del = useMutation({
    mutationFn: () => deleteAddon(project, addon),
    onSuccess: () => {
      toast.success(`Addon ${addon} deleted`);
      qc.invalidateQueries({ queryKey: ["projects", project] });
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
      onClose();
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Delete failed"),
  });
  return (
    <div className="space-y-4 p-5">
      <ConfigurationSection project={project} addon={addon} cr={cr} />
      <PlacementSection project={project} addon={addon} cr={cr} />
      <section className="rounded-md border border-red-500/30 bg-red-500/5 p-4">
        <h4 className="text-sm font-semibold">Delete addon</h4>
        <p className="mt-1 text-xs text-[var(--text-secondary)]">
          Removes the addon and tears down the Helm release. The PVC + data go with it
          unless your storage class retains it. There is no undo.
        </p>
        {!confirming ? (
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
            onClick={() => setConfirming(true)}
          >
            <Trash2 className="h-3.5 w-3.5" /> Delete addon
          </Button>
        ) : (
          <div className="mt-3 space-y-2">
            <p className="text-xs">
              Type <span className="font-mono">{addon}</span> to confirm.
            </p>
            <Input
              value={text}
              onChange={(e) => setText(e.target.value)}
              className="font-mono text-sm"
              autoFocus
            />
            <div className="flex gap-2">
              <Button
                variant="destructive"
                size="sm"
                disabled={text !== addon || del.isPending}
                onClick={() => del.mutate()}
              >
                {del.isPending ? "Deleting…" : "Confirm delete"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => {
                  setConfirming(false);
                  setText("");
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

// ConfigurationSection lets the operator change the addon's tier
// (size), engine version, HA toggle, and storage size after the
// initial create. The fields are pulled from the live CR so the
// form always reflects current state. Save sends a PATCH.
function ConfigurationSection({
  project,
  addon,
  cr,
}: {
  project: string;
  addon: string;
  cr?: import("@/types/projects").KusoAddon;
}) {
  const qc = useQueryClient();
  const initial = {
    version: cr?.spec.version ?? "",
    size: (cr?.spec.size ?? "small") as "small" | "medium" | "large",
    ha: !!cr?.spec.ha,
    storageSize: cr?.spec.storageSize ?? "",
    database: cr?.spec.database ?? "",
  };

  const [version, setVersion] = useState(initial.version);
  const [size, setSize] = useState<typeof initial.size>(initial.size);
  const [ha, setHa] = useState(initial.ha);
  const [storageSize, setStorageSize] = useState(initial.storageSize);
  const [database, setDatabase] = useState(initial.database);

  // Re-baseline whenever the CR changes (e.g. after a successful save
  // or a parallel edit landed on the server). Without this, the form
  // would diverge from the source of truth and the dirty indicator
  // would lie.
  useEffect(() => {
    setVersion(initial.version);
    setSize(initial.size);
    setHa(initial.ha);
    setStorageSize(initial.storageSize);
    setDatabase(initial.database);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    initial.version,
    initial.size,
    initial.ha,
    initial.storageSize,
    initial.database,
  ]);

  const dirty =
    version !== initial.version ||
    size !== initial.size ||
    ha !== initial.ha ||
    storageSize !== initial.storageSize ||
    database !== initial.database;

  const save = useMutation({
    mutationFn: () => {
      // Only include changed fields in the patch — that way unset
      // fields can stay unset on the CR rather than being clobbered
      // with empty strings.
      const body: UpdateAddonBody = {};
      if (version !== initial.version) body.version = version.trim();
      if (size !== initial.size) body.size = size;
      if (ha !== initial.ha) body.ha = ha;
      if (storageSize !== initial.storageSize) body.storageSize = storageSize.trim();
      if (database !== initial.database) body.database = database.trim();
      return updateAddon(project, addon, body);
    },
    onSuccess: () => {
      toast.success("Configuration saved");
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  return (
    <section className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex items-center gap-2">
          <Settings className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          <h4 className="text-sm font-semibold">Configuration</h4>
        </div>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {dirty ? "unsaved" : "in sync"}
        </span>
      </header>
      <div className="space-y-3 p-3">
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1">
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Version
            </label>
            <Input
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              placeholder="leave empty for chart default"
              className="h-7 font-mono text-[12px]"
            />
          </div>
          <div className="space-y-1">
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Tier
            </label>
            <select
              value={size}
              onChange={(e) => setSize(e.target.value as "small" | "medium" | "large")}
              className="h-7 w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[12px] outline-none focus:border-[var(--border-strong)]"
            >
              <option value="small">small</option>
              <option value="medium">medium</option>
              <option value="large">large</option>
            </select>
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div className="space-y-1">
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Storage size
            </label>
            <Input
              value={storageSize}
              onChange={(e) => setStorageSize(e.target.value)}
              placeholder="e.g. 10Gi"
              className="h-7 font-mono text-[12px]"
            />
            <p className="font-mono text-[10px] text-[var(--text-tertiary)]">
              Note: changing this on a running addon requires manual PVC resize.
            </p>
          </div>
          <div className="space-y-1">
            <label className="font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
              Default database
            </label>
            <Input
              value={database}
              onChange={(e) => setDatabase(e.target.value)}
              placeholder="defaults to project name"
              className="h-7 font-mono text-[12px]"
            />
          </div>
        </div>
        <label className="flex cursor-pointer items-center gap-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2">
          <input
            type="checkbox"
            checked={ha}
            onChange={(e) => setHa(e.target.checked)}
            className="h-3.5 w-3.5 accent-[var(--accent)]"
          />
          <span className="text-[12px] font-medium">High availability</span>
          <span className="ml-auto font-mono text-[10px] text-[var(--text-tertiary)]">
            multi-replica · primary/replica streaming
          </span>
        </label>
      </div>
      <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-3 py-2">
        <Button
          size="sm"
          variant="ghost"
          disabled={!dirty || save.isPending}
          onClick={() => {
            setVersion(initial.version);
            setSize(initial.size);
            setHa(initial.ha);
            setStorageSize(initial.storageSize);
            setDatabase(initial.database);
          }}
        >
          Reset
        </Button>
        <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save configuration"}
        </Button>
      </footer>
    </section>
  );
}

// PlacementSection edits spec.placement on a KusoAddon. Mirrors the
// shape of ServiceSettingsPanel's PlacementSection: a header strip
// with a hint badge, native selects driven by what the cluster
// actually carries, a pill-list of specific hostnames, and a live
// match preview. Server validates the selector matches ≥1 node at
// save time and 400s with "no cluster node matches placement" if not.
function PlacementSection({
  project,
  addon,
  cr,
}: {
  project: string;
  addon: string;
  cr?: import("@/types/projects").KusoAddon;
}) {
  const qc = useQueryClient();
  const initialLabels: Record<string, string> =
    (cr?.spec as { placement?: { labels?: Record<string, string> } })?.placement
      ?.labels ?? {};
  const initialNodes: string[] =
    (cr?.spec as { placement?: { nodes?: string[] } })?.placement?.nodes ?? [];

  const nodesQuery = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    staleTime: 30_000,
  });

  const [labels, setLabels] = useState<{ key: string; value: string }[]>(
    Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v })),
  );
  const [pickedNodes, setPickedNodes] = useState<string[]>(initialNodes);

  // Re-baseline when the CR changes (after a save lands).
  useEffect(() => {
    setLabels(Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v })));
    setPickedNodes(initialNodes);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(initialLabels), JSON.stringify(initialNodes)]);

  // Catalog of label keys + values that real cluster nodes carry, so
  // the rule editor offers them as native selects (no typo path).
  const allLabels = new Map<string, Set<string>>();
  for (const n of nodesQuery.data ?? []) {
    for (const [k, v] of Object.entries(n.kusoLabels ?? {})) {
      if (!allLabels.has(k)) allLabels.set(k, new Set());
      allLabels.get(k)!.add(v);
    }
  }
  const allHostnames = (nodesQuery.data ?? []).map((n) => n.name);

  // Live match preview — same logic as the service variant. Empty rules
  // schedule everywhere; a partial rule (key set but no value) doesn't
  // skew the count.
  const matching = (nodesQuery.data ?? []).filter((n) => {
    for (const r of labels) {
      if (!r.key.trim()) continue;
      if ((n.kusoLabels ?? {})[r.key.trim()] !== r.value) return false;
    }
    if (pickedNodes.length > 0 && !pickedNodes.includes(n.name)) return false;
    return true;
  });
  const totalNodes = (nodesQuery.data ?? []).length;
  const incompleteRules = labels.filter((r) => !r.key.trim() || !r.value.trim()).length;
  const hasEffectiveRules =
    labels.some((r) => r.key.trim() && r.value.trim()) || pickedNodes.length > 0;

  const save = useMutation({
    mutationFn: () => {
      const lbls: Record<string, string> = {};
      for (const r of labels) {
        if (r.key.trim() && r.value.trim()) lbls[r.key.trim()] = r.value.trim();
      }
      return setAddonPlacement(project, addon, { labels: lbls, nodes: pickedNodes });
    },
    onSuccess: () => {
      toast.success("Placement saved");
      qc.invalidateQueries({ queryKey: ["projects", project, "addons"] });
    },
    onError: (e) => toast.error(e instanceof Error ? e.message : "Save failed"),
  });

  const dirty =
    JSON.stringify(
      Object.fromEntries(
        labels
          .filter((r) => r.key.trim() && r.value.trim())
          .map((r) => [r.key.trim(), r.value.trim()]),
      ),
    ) !== JSON.stringify(initialLabels) ||
    JSON.stringify(pickedNodes) !== JSON.stringify(initialNodes);

  const addLabel = () => setLabels((cur) => [...cur, { key: "", value: "" }]);
  const updLabel = (i: number, patch: Partial<{ key: string; value: string }>) =>
    setLabels((cur) => cur.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const rmLabel = (i: number) => setLabels((cur) => cur.filter((_, j) => j !== i));
  const toggleNode = (name: string) =>
    setPickedNodes((cur) =>
      cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name],
    );

  return (
    <section className="overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)]">
      <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-3 py-2">
        <div className="flex items-center gap-2">
          <MapPin className="h-3.5 w-3.5 text-[var(--text-tertiary)]" />
          <h4 className="text-sm font-semibold">Placement</h4>
        </div>
        <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
          {!hasEffectiveRules
            ? "schedules anywhere"
            : incompleteRules > 0
              ? `${incompleteRules} incomplete`
              : `${matching.length}/${totalNodes} match`}
        </span>
      </header>
      <p className="border-b border-[var(--border-subtle)] px-3 py-2 text-[11px] text-[var(--text-secondary)]">
        Pin this addon to a subset of cluster nodes. Use{" "}
        <span className="font-mono text-[var(--text-tertiary)]">key=value</span> rules
        (e.g. <span className="font-mono">region=eu</span> or{" "}
        <span className="font-mono">tier=db</span>) to match nodes by label, or pick
        specific hostnames below. Empty rules schedule anywhere.
      </p>
      {labels.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
          No label rules.
        </p>
      ) : (
        labels.map((r, i) => {
          const valuesForKey = allLabels.get(r.key.trim());
          const haveAnyLabels = allLabels.size > 0;
          return (
            <div
              key={i}
              className="grid grid-cols-[140px_1fr_28px] items-center gap-1.5 border-b border-[var(--border-subtle)] px-3 py-1.5 last:border-b-0"
            >
              {haveAnyLabels ? (
                <select
                  value={r.key}
                  onChange={(e) => updLabel(i, { key: e.target.value, value: "" })}
                  className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
                >
                  <option value="">pick label key</option>
                  {[...allLabels.keys()].sort().map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </select>
              ) : (
                <Input
                  value={r.key}
                  onChange={(e) => updLabel(i, { key: e.target.value })}
                  placeholder="region"
                  className="h-7 font-mono text-[11px]"
                />
              )}
              {valuesForKey && valuesForKey.size > 0 ? (
                <select
                  value={r.value}
                  onChange={(e) => updLabel(i, { value: e.target.value })}
                  className="h-7 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 font-mono text-[11px] text-[var(--text-primary)] outline-none focus:border-[var(--border-strong)]"
                >
                  <option value="">pick value</option>
                  {[...valuesForKey].sort().map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>
              ) : (
                <Input
                  value={r.value}
                  onChange={(e) => updLabel(i, { value: e.target.value })}
                  placeholder={r.key.trim() ? "value" : "eu"}
                  className="h-7 font-mono text-[11px]"
                  disabled={haveAnyLabels && !r.key.trim()}
                />
              )}
              <button
                type="button"
                onClick={() => rmLabel(i)}
                aria-label="Remove rule"
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-red-400"
              >
                <X className="h-3 w-3" />
              </button>
            </div>
          );
        })
      )}
      <button
        type="button"
        onClick={addLabel}
        className="flex w-full items-center gap-1.5 border-y border-[var(--border-subtle)] px-3 py-2 text-left text-[11px] text-[var(--accent)] hover:bg-[var(--bg-tertiary)]/40"
      >
        <Plus className="h-3 w-3" />
        add label rule
      </button>

      <div className="px-3 py-2">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-[12px] text-[var(--text-secondary)]">
            specific nodes
          </span>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {pickedNodes.length === 0
              ? "any node matching the labels"
              : `${pickedNodes.length} pinned`}
          </span>
        </div>
        {allHostnames.length === 0 ? (
          <p className="text-[11px] text-[var(--text-tertiary)]">
            No nodes visible yet.
          </p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {allHostnames.map((h) => {
              const picked = pickedNodes.includes(h);
              return (
                <button
                  key={h}
                  type="button"
                  onClick={() => toggleNode(h)}
                  className={cn(
                    "inline-flex h-6 items-center rounded-md border px-2 font-mono text-[10px] transition-colors",
                    picked
                      ? "border-[var(--accent)]/40 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-tertiary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {h}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {hasEffectiveRules && (
        <div className="border-t border-[var(--border-subtle)] px-3 py-2 text-[10px] text-[var(--text-tertiary)]">
          {incompleteRules > 0 ? (
            <span>
              fill in the {incompleteRules === 1 ? "empty rule" : "empty rules"} or
              remove {incompleteRules === 1 ? "it" : "them"} to see what matches
            </span>
          ) : (
            <span>
              {matching.length}/{totalNodes} cluster nodes match this placement
            </span>
          )}
        </div>
      )}

      <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-3 py-2">
        <Button
          size="sm"
          variant="ghost"
          disabled={!dirty || save.isPending}
          onClick={() => {
            setLabels(
              Object.entries(initialLabels).map(([k, v]) => ({ key: k, value: v })),
            );
            setPickedNodes(initialNodes);
          }}
        >
          Reset
        </Button>
        <Button size="sm" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          {save.isPending ? "Saving…" : "Save placement"}
        </Button>
      </footer>
    </section>
  );
}
