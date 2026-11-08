"use client";

import { useQuery } from "@tanstack/react-query";
import { Check, MapPin, Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api-client";
import { cn } from "@/lib/utils";
import {
  Section,
  Field,
  Callout,
  Code,
  monoInputClass,
  selectClass,
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
  // A row with a value is exact-match; a row with a key but no value is a
  // presence-only capability flag (e.g. `gpu`) — the node just has to
  // carry the key, any value. Mirrors placement.Matches on the server.
  const matching = (nodes.data ?? []).filter((n) => {
    for (const r of state.placement) {
      const key = r.key.trim();
      if (!key) continue;
      const labels = n.kusoLabels ?? {};
      if (r.value.trim() === "") {
        if (!(key in labels)) return false;
      } else if (labels[key] !== r.value) {
        return false;
      }
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

  // Treat rows with no KEY as "no rule yet" rather than letting them skew
  // the live match count. A blank value is NOT incomplete — a key-only row
  // is a valid presence-only capability flag (e.g. `gpu`); it matches any
  // node carrying that key. Only a missing key means the row hasn't taken
  // effect yet.
  const incompleteRules = state.placement.filter((r) => !r.key.trim()).length;
  const hasEffectiveRules =
    state.placement.some((r) => r.key.trim()) ||
    state.placementNodes.length > 0;

  const status = !hasEffectiveRules
    ? "Any node"
    : incompleteRules > 0
      ? `${incompleteRules} incomplete rule${incompleteRules === 1 ? "" : "s"}`
      : `${matching.length} of ${totalNodes} nodes match`;
  const haveAnyLabels = allLabels.size > 0;

  return (
    <Section
      id="placement"
      title="Placement"
      icon={MapPin}
      status={status}
      description={
        <>
          Pin this service to some cluster nodes, by label (e.g. <Code>region=eu</Code>) or by hostname. A node must
          satisfy every rule. No rules schedules anywhere.
        </>
      }
      footerNote="Saving moves pods that sit on non-matching nodes."
    >
      <Field
        label="Label rules"
        hint="Leave the value blank to match any node that carries the key."
      >
        <div className="space-y-2">
          {state.placement.length === 0 && (
            <p className="text-xs text-[var(--text-tertiary)]">No label rules.</p>
          )}
          {state.placement.map((r, i) => {
            const valuesForKey = allLabels.get(r.key.trim());
            return (
              // Key on (key, value) so removing a non-last rule doesn't
              // leave the survivor's controlled select showing the
              // deleted row's value.
              <div
                key={r.key ? `k:${r.key}=${r.value}` : `empty:${i}`}
                className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_32px] items-center gap-2"
              >
                {haveAnyLabels ? (
                  <select
                    value={r.key}
                    onChange={(e) => updLabel(i, { key: e.target.value, value: "" })}
                    aria-label="Label key"
                    className={cn(selectClass, "font-mono text-[12px]")}
                  >
                    <option value="">Pick a label key</option>
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
                    placeholder="e.g. region"
                    aria-label="Label key"
                    className={monoInputClass}
                  />
                )}
                {valuesForKey && valuesForKey.size > 0 ? (
                  <select
                    value={r.value}
                    onChange={(e) => updLabel(i, { value: e.target.value })}
                    aria-label="Label value"
                    className={cn(selectClass, "font-mono text-[12px]")}
                  >
                    <option value="">Any value</option>
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
                    placeholder={r.key.trim() ? "any value" : "e.g. eu"}
                    aria-label="Label value"
                    className={monoInputClass}
                    disabled={haveAnyLabels && !r.key.trim()}
                  />
                )}
                <button
                  type="button"
                  onClick={() => rmLabel(i)}
                  aria-label="Remove rule"
                  className="inline-flex h-8 w-8 items-center justify-center rounded-sm text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--error)]"
                >
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            );
          })}
          <Button type="button" variant="outline" size="xs" onClick={addLabel}>
            <Plus className="h-3 w-3" />
            Add rule
          </Button>
        </div>
      </Field>

      <Field
        label="Specific nodes"
        hint={
          state.placementNodes.length === 0
            ? "None picked: any node matching the label rules."
            : `${state.placementNodes.length} pinned. Pods only run on these nodes.`
        }
      >
        {allHostnames.length === 0 ? (
          <p className="text-xs text-[var(--text-tertiary)]">No nodes visible.</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {allHostnames.map((h) => {
              const picked = state.placementNodes.includes(h);
              return (
                <button
                  key={h}
                  type="button"
                  aria-pressed={picked}
                  onClick={() => toggleNode(h)}
                  className={cn(
                    "inline-flex h-7 items-center gap-1.5 rounded-md border px-2.5 font-mono text-[12px] transition-colors",
                    picked
                      ? "border-[var(--accent)]/50 bg-[var(--accent-subtle)] text-[var(--text-primary)]"
                      : "border-[var(--border-subtle)] bg-[var(--bg-primary)] text-[var(--text-secondary)] hover:text-[var(--text-primary)]",
                  )}
                >
                  {picked && <Check className="h-3 w-3" />}
                  {h}
                </button>
              );
            })}
          </div>
        )}
      </Field>

      {/* Live match preview, only once a rule is complete or a node is
          pinned; an unfinished blank row would show a misleading count. */}
      {hasEffectiveRules &&
        (incompleteRules > 0 ? (
          <Callout>
            Fill in or remove the {incompleteRules === 1 ? "empty rule" : "empty rules"} to see which nodes match.
          </Callout>
        ) : matching.length === 0 ? (
          <Callout tone="warning">No node matches these rules, so pods would stay Pending.</Callout>
        ) : (
          <Callout>
            Would schedule on{" "}
            <span className="font-mono text-[var(--text-primary)]">{matching.map((m) => m.name).join(", ")}</span>.
          </Callout>
        ))}
    </Section>
  );
}
