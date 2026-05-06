"use client";

import { useQuery } from "@tanstack/react-query";
import { MapPin, Plus, X } from "lucide-react";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import {
  Section,
  type NodeSummary,
  type PlacementRow,
  type SectionProps,
} from "./_primitives";

export function PlacementSection({ state, setState }: SectionProps) {
  // Pull live nodes so the user can pick from real labels + hostnames
  // instead of guessing. We don't gate the section on the response —
  // empty cluster means a friendlier "no nodes labeled yet" hint.
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    staleTime: 30_000,
  });
  const allLabels = new Map<string, Set<string>>();
  for (const n of nodes.data ?? []) {
    for (const [k, v] of Object.entries(n.kusoLabels ?? {})) {
      if (!allLabels.has(k)) allLabels.set(k, new Set());
      allLabels.get(k)!.add(v);
    }
  }
  const allHostnames = (nodes.data ?? []).map((n) => n.name);

  // Live preview: which nodes match the current label+nodes selection?
  const matching = (nodes.data ?? []).filter((n) => {
    for (const r of state.placement) {
      if (!r.key.trim()) continue;
      if ((n.kusoLabels ?? {})[r.key.trim()] !== r.value) return false;
    }
    if (state.placementNodes.length > 0 && !state.placementNodes.includes(n.name))
      return false;
    return true;
  });

  const addLabel = () =>
    setState((s) => ({ ...s, placement: [...s.placement, { key: "", value: "" }] }));
  const updLabel = (i: number, patch: Partial<PlacementRow>) =>
    setState((s) => ({
      ...s,
      placement: s.placement.map((r, j) => (j === i ? { ...r, ...patch } : r)),
    }));
  const rmLabel = (i: number) =>
    setState((s) => ({ ...s, placement: s.placement.filter((_, j) => j !== i) }));

  const toggleNode = (name: string) =>
    setState((s) => ({
      ...s,
      placementNodes: s.placementNodes.includes(name)
        ? s.placementNodes.filter((n) => n !== name)
        : [...s.placementNodes, name],
    }));

  const totalNodes = (nodes.data ?? []).length;

  // Treat unfilled rule rows as "no rule yet" rather than letting them
  // skew the live match count. Without this, opening "+ add label rule"
  // and leaving the row blank reads "1/1 match" because the empty key
  // was being skipped — so the user couldn't tell if the rule had taken
  // effect or not. Trivially-true rows now show as "incomplete".
  const incompleteRules = state.placement.filter(
    (r) => !r.key.trim() || !r.value.trim(),
  ).length;
  const hasEffectiveRules =
    state.placement.some((r) => r.key.trim() && r.value.trim()) ||
    state.placementNodes.length > 0;

  return (
    <Section
      id="placement"
      title="Placement"
      icon={MapPin}
      hint={
        !hasEffectiveRules
          ? "schedules anywhere"
          : incompleteRules > 0
            ? `${incompleteRules} incomplete`
            : `${matching.length}/${totalNodes} match`
      }
    >
      <p className="border-b border-[var(--border-subtle)] px-3 py-2 text-[11px] text-[var(--text-secondary)]">
        Pin this service to a subset of cluster nodes. Use{" "}
        <span className="font-mono text-[var(--text-tertiary)]">key=value</span> rules
        (e.g. <span className="font-mono">region=eu</span> or{" "}
        <span className="font-mono">tier=gpu</span>) to match nodes by label, or pick
        specific hostnames below. Nodes need both — empty rules schedule anywhere.
      </p>
      {/* Label rules — native selects backed by what the cluster
          actually carries (kusoLabels off /api/kubernetes/nodes). When
          a cluster has no labels yet we fall back to free-text inputs
          so the user can still author rules ahead of labeling nodes. */}
      {state.placement.length === 0 ? (
        <p className="px-3 py-2.5 text-[11px] text-[var(--text-tertiary)]">
          No label rules.
        </p>
      ) : (
        state.placement.map((r, i) => {
          const valuesForKey = allLabels.get(r.key.trim());
          const haveAnyLabels = allLabels.size > 0;
          return (
            // Key on (key, value) so removing a non-last placement
            // rule doesn't leave the survivor's controlled select
            // displaying the deleted row's value. Index keys broke
            // this — same class as the F-2 audit finding.
            <div
              key={r.key ? `k:${r.key}=${r.value}` : `empty:${i}`}
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

      {/* Specific-node pinning */}
      <div className="px-3 py-2">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-[12px] text-[var(--text-secondary)]">
            specific nodes
          </span>
          <span className="font-mono text-[10px] text-[var(--text-tertiary)]">
            {state.placementNodes.length === 0
              ? "any node matching the labels"
              : `${state.placementNodes.length} pinned`}
          </span>
        </div>
        {allHostnames.length === 0 ? (
          <p className="text-[11px] text-[var(--text-tertiary)]">
            No nodes visible yet.
          </p>
        ) : (
          <div className="flex flex-wrap gap-1">
            {allHostnames.map((h) => {
              const picked = state.placementNodes.includes(h);
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

      {/* Live match preview — only meaningful once there's at least
          one fully-filled rule or a pinned hostname. Showing it for an
          unfinished blank row was the source of the "1/1 match"
          confusion in the screenshots. */}
      {hasEffectiveRules && (
        <div className="border-t border-[var(--border-subtle)] px-3 py-2 text-[10px] text-[var(--text-tertiary)]">
          {incompleteRules > 0 ? (
            <span className="text-[var(--text-tertiary)]">
              fill in the {incompleteRules === 1 ? "empty rule" : "empty rules"} or
              remove {incompleteRules === 1 ? "it" : "them"} to see what matches
            </span>
          ) : matching.length === 0 ? (
            <span className="text-amber-400">
              ⚠ No nodes match. Pods would stay Pending.
            </span>
          ) : (
            <span>
              would schedule on:{" "}
              <span className="font-mono text-[var(--text-secondary)]">
                {matching.map((m) => m.name).join(", ")}
              </span>
            </span>
          )}
        </div>
      )}
    </Section>
  );
}
