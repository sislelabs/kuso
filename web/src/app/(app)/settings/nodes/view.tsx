"use client";

import { SettingsPage } from "@/components/settings/SettingsPage";
import { SettingsNotice, SettingsSelect } from "@/components/settings/SettingsSection";
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { motion, AnimatePresence } from "motion/react";
import { api, ApiError } from "@/lib/api-client";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { toastError } from "@/lib/errors";
import { ChevronRight, Plus, X, Save, MapPin, Tag, Trash2, Package, RotateCcw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { formatCpu, relativeTime } from "@/lib/format";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { useCan, Perms } from "@/features/auth";
import type { NodeSummary } from "@/components/layout/ServersPopover";
import { DbHealthTile } from "@/components/shared/DbHealthTile";

interface Label {
  key: string;
  value: string;
}

// NodeUpdateAdvisory mirrors the server's pkgupdates.Advisory — the
// host package-update summary the pkg-probe DaemonSet writes per node.
interface NodeUpdateAdvisory {
  node: string;
  count: number;
  rebootRequired: boolean;
  pkgMgr: string;
  sample: string[];
  checkedAt: string;
  present: boolean;
  apply: { phase: string; at: string; log: string };
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
  // The nodes API is admin-only (server: requireAdmin → settings:admin).
  // Gate the queries on that so a non-admin who lands here directly
  // (bookmark, typed URL) never fires a request that 403s and shows the
  // raw "Failed to load nodes: forbidden" — we render a friendly notice
  // instead. The settings landing card is also perm-gated to match.
  const isAdmin = useCan(Perms.SettingsAdmin);
  const nodes = useQuery({
    queryKey: ["kubernetes", "nodes"],
    queryFn: () => api<NodeSummary[]>("/api/kubernetes/nodes"),
    enabled: isAdmin,
  });
  // Host package-update advisory per node (from the pkg-probe DaemonSet
  // annotations). Separate query so a slow/absent probe never blocks the
  // node list. Keyed by node name for per-card lookup.
  const updates = useQuery({
    queryKey: ["kubernetes", "node-updates"],
    queryFn: () =>
      api<{ data: NodeUpdateAdvisory[] }>("/api/kubernetes/nodes/updates").then((r) => r.data ?? []),
    staleTime: 60_000,
    enabled: isAdmin,
  });
  const advisoryByNode = new Map((updates.data ?? []).map((a) => [a.node, a]));

  const [addOpen, setAddOpen] = useState(false);
  const [cleanupOpen, setCleanupOpen] = useState(false);
  // Cleanup mutation — deletes finished pods + jobs across the
  // cluster. Admin-only on the server; the UI just hides the
  // button when the user lacks the perm.
  const cleanup = useMutation({
    mutationFn: () =>
      api<{ podsDeleted: number; jobsDeleted: number; namespaces: string[]; errors?: string[] }>(
        "/api/kubernetes/cleanup-completed",
        { method: "POST" },
      ),
    onSuccess: (res) => {
      setCleanupOpen(false);
      const errs = res.errors?.length ?? 0;
      const summary = `Cleaned up ${res.podsDeleted} pod${res.podsDeleted === 1 ? "" : "s"} + ${res.jobsDeleted} job${res.jobsDeleted === 1 ? "" : "s"}`;
      if (errs) {
        toast.warning(`${summary} (${errs} error${errs === 1 ? "" : "s"} — check server logs)`);
      } else if (res.podsDeleted + res.jobsDeleted === 0) {
        toast.success("Nothing to clean up — host is already tidy");
      } else {
        toast.success(summary);
      }
      qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    },
    onError: (e) => toastError(e, "Cleanup failed"),
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
      const warnings: string[] = [];
      for (const [nodeName, labels] of Object.entries(edits)) {
        const body = {
          labels: Object.fromEntries(
            labels.filter((l) => l.key.trim()).map((l) => [l.key.trim(), l.value])
          ),
        };
        // 200 + {warning} = labels landed but the region taint didn't.
        const res = await api<{ warning?: string } | undefined>(
          `/api/kubernetes/nodes/${encodeURIComponent(nodeName)}/labels`,
          { method: "PUT", body },
        );
        if (res?.warning) warnings.push(`${nodeName}: ${res.warning}`);
      }
      if (warnings.length > 0) {
        for (const w of warnings) toast.warning(w);
      } else {
        toast.success(
          Object.keys(edits).length === 1
            ? "Node updated"
            : `${Object.keys(edits).length} nodes updated`
        );
      }
      setEdits({});
      await qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    } catch (e) {
      toastError(e, "Failed to save");
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

  if (!isAdmin) {
    return (
      <SettingsPage title="Nodes">
        <SettingsNotice tone="neutral">Cluster nodes are admin-only. Ask a team admin to enable it for you.</SettingsNotice>
      </SettingsPage>
    );
  }

  return (
    <SettingsPage
      className="pb-24"
      title="Nodes"
      description={
        <>
          Tag nodes with labels (e.g. <span className="font-mono">region=eu</span>,{" "}
          <span className="font-mono">tier=premium</span>) and projects can pin to them. Bare keys without a value
          work too, for capability flags like <span className="font-mono">gpu</span>.
        </>
      }
      actions={
        <>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setCleanupOpen(true)}
            disabled={cleanup.isPending}
            className="shrink-0"
            title="Delete finished pods + jobs across the cluster. Frees up scheduling slots and cgroup state on a packed host."
          >
            <Trash2 className="h-3.5 w-3.5" />
            {cleanup.isPending ? "Cleaning…" : "Cleanup"}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setAddOpen(true)}
            className="shrink-0"
          >
            <Plus className="h-3.5 w-3.5" />
            Add node
          </Button>
        </>
      }
    >
      <ConfirmDialog
        open={cleanupOpen}
        title="Clean up finished pods and jobs?"
        body={<p>Deletes finished pods and jobs on every node, along with their logs.</p>}
        confirmLabel="Clean up"
        pending={cleanup.isPending}
        onConfirm={() => cleanup.mutate()}
        onCancel={() => setCleanupOpen(false)}
      />
      {addOpen && (
        <AddNodeModal
          onClose={() => setAddOpen(false)}
          onJoined={() => {
            // Don't auto-close here — bootstrap flow shows a Done
            // button so the operator can confirm the install scrolled
            // on the VM. Just refresh the node list in the background.
            qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
            qc.invalidateQueries({ queryKey: ["node-bootstrap", "pending"] });
          }}
        />
      )}

      {/* Pending bootstrap tokens — shows tokens the operator minted
          that the new VM hasn't redeemed yet. Empty state collapses
          the card so it doesn't clutter the page. */}
      <PendingBootstrapTokensCard />

      {nodes.isPending ? (
        <div className="space-y-3">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : nodes.isError ? (
        <SettingsNotice tone="error" title="Failed to load nodes">
          {nodes.error?.message}
        </SettingsNotice>
      ) : (
        <ul className="space-y-3">
          {(nodes.data ?? []).map((n) => (
            <NodeCard
              key={n.name}
              node={n}
              labels={labelsFor(n)}
              onChange={(next) => setLabels(n, next)}
              isDirty={n.name in edits}
              advisory={advisoryByNode.get(n.name)}
            />
          ))}
        </ul>
      )}

      {/* DB write-lock health. SQLite is single-writer; a sustained
          busy count means the control plane is saturated. Lives here
          because /settings/nodes is the operational-health page and
          this is a node-adjacent process-level signal. */}
      <DbHealthTile />

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
    </SettingsPage>
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
          <span className="mr-auto inline-flex items-center gap-1.5 text-xs text-[var(--text-secondary)]">
            <span aria-hidden className="inline-block h-1.5 w-1.5 rounded-full bg-[var(--warning)]" />
            Unsaved label changes on {count} {count === 1 ? "node" : "nodes"}
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
  advisory,
}: {
  node: NodeSummary;
  labels: Label[];
  onChange: (next: Label[]) => void;
  isDirty: boolean;
  advisory?: NodeUpdateAdvisory;
}) {
  const status = node.unreachable
    ? { text: "Unreachable", tone: "danger" as const, title: "kuso cordoned this node because it has been NotReady past the threshold. It is uncordoned automatically when the node recovers." }
    : !node.ready
      ? { text: "Not ready", tone: "danger" as const, title: "The kubelet on this node isn't reporting Ready." }
      : !node.schedulable
        ? { text: "Cordoned", tone: "warning" as const, title: "New pods won't be scheduled here." }
        : { text: "Ready", tone: "success" as const, title: undefined };
  return (
    <li
      className={cn(
        "overflow-hidden rounded-lg border bg-[var(--bg-secondary)] transition-colors",
        isDirty ? "border-[var(--warning)]/50" : "border-[var(--border-subtle)]"
      )}
    >
      <header className="flex items-start gap-3 px-4 pt-4 sm:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <h3 className="truncate font-mono text-sm font-semibold text-[var(--text-primary)]">{node.name}</h3>
            <Badge variant={status.tone} title={status.title}>
              <span aria-hidden className="size-1.5 rounded-full bg-current" />
              {status.text}
            </Badge>
            {isDirty && <Badge variant="warning">Unsaved</Badge>}
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-tertiary)]">
            {node.roles.map((r) => (
              <span key={r}>{roleLabel(r)}</span>
            ))}
            {node.zone && <span>Zone <span className="font-mono">{node.zone}</span></span>}
            {!node.schedulable && !node.unreachable && <UncordonButton node={node.name} />}
          </div>
        </div>
        {/* Hide Remove on the control plane — the server refuses
            anyway, no point teasing the button. */}
        {!node.roles.includes("control-plane") && <RemoveNodeButton node={node} />}
      </header>

      <div className="space-y-3 px-4 pt-4 pb-4 sm:px-5">
        <NodeStats node={node} />
        <PackageUpdates advisory={advisory} />
      </div>

      {/* The chip strip is the entire label UI: the chips ARE the
          labels, and adding/removing happens here too. */}
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2 border-t border-[var(--border-subtle)] bg-[var(--bg-primary)]/40 px-4 py-3 sm:px-5">
        <span className="pt-1.5 text-xs font-medium text-[var(--text-secondary)]">Labels</span>
        <ChipStrip labels={labels} onChange={onChange} />
      </div>
    </li>
  );
}

function roleLabel(role: string): string {
  if (role === "control-plane" || role === "master") return "Control plane";
  if (role === "worker") return "Worker";
  return role.charAt(0).toUpperCase() + role.slice(1).replace(/-/g, " ");
}

// PackageUpdates renders the per-node host package-update advisory plus
// the apply action. States: not-yet-probed, unsupported OS, up-to-date,
// updates-available (with Apply), and an in-flight apply banner
// (running/rebooting/failed) that polls until terminal.
function PackageUpdates({ advisory }: { advisory?: NodeUpdateAdvisory }) {
  const qc = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const phase = advisory?.apply?.phase ?? "";
  // "settling" is the post-reboot sweep; the server 409s a new apply until it ends.
  const inFlight =
    phase === "running" || phase === "draining" || phase === "rebooting" || phase === "settling";

  const apply = useMutation({
    mutationFn: (allowReboot: boolean) =>
      api(`/api/kubernetes/nodes/${encodeURIComponent(advisory!.node)}/apply-updates`, {
        method: "POST",
        body: { allowReboot },
      }),
    onSuccess: () => {
      toast.success(`Applying updates on ${advisory!.node}…`);
      setConfirmOpen(false);
      qc.invalidateQueries({ queryKey: ["kubernetes", "node-updates"] });
    },
    onError: (e) => toastError(e, "Apply failed"),
  });

  // While an apply is in flight, poll the advisory so phase transitions
  // (running → draining → rebooting → done) surface without a manual refresh.
  useEffect(() => {
    if (!inFlight) return;
    const t = setInterval(() => {
      if (document.visibilityState !== "visible") return;
      void qc.invalidateQueries({ queryKey: ["kubernetes", "node-updates"] });
    }, 8000);
    return () => clearInterval(t);
  }, [inFlight, qc]);

  if (!advisory || !advisory.present) {
    return (
      <div className="flex items-center gap-2 text-xs text-[var(--text-tertiary)]">
        <Package className="h-3.5 w-3.5" /> Checking host packages…
      </div>
    );
  }
  if (advisory.pkgMgr === "unsupported" || advisory.pkgMgr === "") {
    return (
      <div className="flex items-center gap-2 text-xs text-[var(--text-tertiary)]">
        <Package className="h-3.5 w-3.5" /> Host package updates aren&apos;t tracked on this OS
      </div>
    );
  }
  const checked = advisory.checkedAt ? relativeTime(advisory.checkedAt) : "—";
  if (advisory.count === 0) {
    return (
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-[var(--text-tertiary)]">
        <span className="inline-flex items-center gap-2 text-[var(--success)]">
          <Package className="h-3.5 w-3.5" /> Host packages up to date
        </span>
        <span>· checked {checked}</span>
        {phase === "done" && advisory.apply.at && <span>· patched {relativeTime(advisory.apply.at)}</span>}
      </div>
    );
  }
  // In-flight apply banner — shown while a patch/reboot is running.
  if (inFlight) {
    return (
      <div className="flex items-center gap-2 rounded-md border border-[var(--info)]/25 bg-[var(--info-subtle)] px-3 py-2 text-xs text-[var(--info)]">
        <RotateCcw className="h-3.5 w-3.5 shrink-0 animate-spin" />
        {phase === "settling"
          ? "Rebooted — waiting for workloads to settle…"
          : phase === "rebooting"
          ? "Patched — rebooting node to finish…"
          : phase === "draining"
            ? "Patched — draining node before reboot…"
            : "Applying host package updates…"}
        <span className="ml-auto text-xs text-[var(--text-tertiary)]">
          {advisory.apply.at ? relativeTime(advisory.apply.at) : ""}
        </span>
      </div>
    );
  }
  return (
    <div className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
        <Package className="h-4 w-4 shrink-0 text-[var(--info)]" aria-hidden />
        <span className="text-[13px] font-medium text-[var(--text-primary)]">
          {advisory.count} host package update{advisory.count === 1 ? "" : "s"}
        </span>
        {advisory.rebootRequired && (
          <Badge variant="neutral" title="A host reboot is required to fully apply these updates.">
            <RotateCcw aria-hidden /> Reboot needed
          </Badge>
        )}
        <span className="text-xs text-[var(--text-tertiary)]">
          <span className="font-mono">{advisory.pkgMgr}</span> · checked {checked}
        </span>
        <Button
          size="sm"
          variant="secondary"
          className="ml-auto"
          onClick={() => setConfirmOpen(true)}
          disabled={apply.isPending}
        >
          <Package aria-hidden /> Apply updates
        </Button>
      </div>
      {advisory.sample.length > 0 && (
        <details className="group mt-2">
          <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded text-xs text-[var(--text-secondary)] hover:text-[var(--text-primary)] [&::-webkit-details-marker]:hidden">
            <ChevronRight aria-hidden className="size-3.5 transition-transform group-open:rotate-90" />
            Show packages
          </summary>
          <ul className="mt-1.5 space-y-0.5 pl-4.5 font-mono text-xs text-[var(--text-secondary)]">
            {advisory.sample.map((s) => (
              <li key={s} className="truncate" title={s}>
                {s}
              </li>
            ))}
            {advisory.count > advisory.sample.length && (
              <li className="font-sans text-[var(--text-tertiary)]">
                …and {advisory.count - advisory.sample.length} more
              </li>
            )}
          </ul>
        </details>
      )}
      {phase === "failed" && (
        <p className="mt-2 truncate text-xs text-[var(--error)]" title={advisory.apply.log}>
          Last apply failed: <span className="font-mono">{advisory.apply.log || "see server logs"}</span>
        </p>
      )}
      <ConfirmDialog
        open={confirmOpen}
        title={`Apply ${advisory.count} update${advisory.count === 1 ? "" : "s"} on ${advisory.node}?`}
        body={
          <div className="space-y-2">
            <p>
              Installs host package updates on <span className="font-mono">{advisory.node}</span>
              {advisory.pkgMgr ? <> using <span className="font-mono">{advisory.pkgMgr}</span></> : null}.
            </p>
            {advisory.rebootRequired && (
              <p className="text-[var(--warning)]">
                These updates require a <strong>reboot</strong>. Confirming will patch and then
                reboot the node — workloads on it restart, and if this is the control-plane node
                the kuso UI will briefly disconnect until it comes back.
              </p>
            )}
          </div>
        }
        confirmLabel={advisory.rebootRequired ? "Patch + reboot" : "Apply updates"}
        destructive={advisory.rebootRequired}
        pending={apply.isPending}
        onConfirm={() => apply.mutate(advisory.rebootRequired)}
        onCancel={() => setConfirmOpen(false)}
      />
    </div>
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
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
      {labels.map((l, i) => (
        <Chip key={`${l.key}-${i}`} label={l} onRemove={() => remove(i)} />
      ))}

      {!adding ? (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="inline-flex h-7 items-center gap-1 rounded-md border border-dashed border-[var(--border-default)] px-2 text-xs text-[var(--text-secondary)] hover:border-[var(--accent)]/40 hover:bg-[var(--accent-subtle)] hover:text-[var(--accent)]"
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
        "group inline-flex h-7 items-center gap-1.5 rounded-md border pr-1 pl-2 font-mono text-xs",
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
        className="inline-flex h-5 w-5 items-center justify-center rounded text-[var(--text-tertiary)] opacity-60 transition-opacity group-hover:opacity-100 hover:bg-[var(--bg-primary)] hover:text-[var(--error)] focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-[var(--focus)] focus-visible:outline-none pointer-coarse:opacity-100"
      >
        <X className="h-3 w-3" />
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
        className="h-6 w-24 border-0 bg-transparent font-mono text-xs focus-visible:ring-0"
      />
      <span className="font-mono text-xs text-[var(--text-tertiary)]/60">=</span>
      <Input
        ref={valueRef}
        value={draftValue}
        onChange={(e) => onChangeValue(e.target.value)}
        onKeyDown={onValueKeyDown}
        placeholder="value"
        className="h-6 w-24 border-0 bg-transparent font-mono text-xs focus-visible:ring-0"
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
        className="inline-flex h-5 w-5 items-center justify-center rounded text-[var(--text-tertiary)] hover:bg-[var(--bg-primary)]/50 hover:text-[var(--error)]"
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
              className="inline-flex h-5 items-center rounded border border-[var(--border-subtle)] bg-[var(--bg-primary)]/60 px-1.5 font-mono text-xs text-[var(--text-tertiary)] hover:border-[var(--accent)]/30 hover:text-[var(--accent)]"
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
// history (sampled every 5 min server-side) so the operator can
// see trends instead of a single point-in-time number. The "metric"
// param tells the modal which row to highlight.
function NodeStats({ node }: { node: NodeSummary }) {
  const [openMetric, setOpenMetric] = useState<NodeMetricKind | null>(null);
  const hasUsage = (node.cpuUsageMilli ?? 0) > 0 || (node.memUsageBytes ?? 0) > 0;
  const pct = (used: number, cap: number | undefined) => (cap ? Math.min(100, Math.round((used / cap) * 100)) : null);
  const diskUsed = (node.diskCapacityBytes ?? 0) - (node.diskAvailableBytes ?? 0);
  return (
    <>
      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-md border border-[var(--border-subtle)] bg-[var(--border-subtle)] lg:grid-cols-4">
        <Stat
          label="CPU"
          detail={
            hasUsage
              ? `${formatCpu(node.cpuUsageMilli ?? 0)} of ${formatCpu(node.cpuCapacityMilli ?? 0)}`
              : formatCpu(node.cpuCapacityMilli ?? 0)
          }
          pct={hasUsage ? pct(node.cpuUsageMilli ?? 0, node.cpuCapacityMilli) : null}
          onClick={() => setOpenMetric("cpu")}
        />
        <Stat
          label="Memory"
          detail={
            hasUsage
              ? `${formatBytes(node.memUsageBytes ?? 0)} of ${formatBytes(node.memCapacityBytes ?? 0)}`
              : formatBytes(node.memCapacityBytes ?? 0)
          }
          pct={hasUsage ? pct(node.memUsageBytes ?? 0, node.memCapacityBytes) : null}
          onClick={() => setOpenMetric("mem")}
        />
        <Stat
          label="Disk"
          detail={
            node.diskCapacityBytes
              ? `${formatBytes(diskUsed)} of ${formatBytes(node.diskCapacityBytes)}`
              : "Not reported"
          }
          pct={node.diskAvailableBytes !== undefined ? pct(diskUsed, node.diskCapacityBytes) : null}
          onClick={() => setOpenMetric("disk")}
        />
        <Stat
          label="Pods"
          detail={node.podsCapacity ? `${node.pods ?? 0} of ${node.podsCapacity} slots` : "Not reported"}
          pct={node.podsCapacity ? pct(node.pods ?? 0, node.podsCapacity) : null}
        />
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

// A capacity tile: big percentage, a pressure-coloured bar and the raw
// numbers. Tiles with history open the 7-day drill-down on click.
function Stat({
  label,
  detail,
  pct,
  onClick,
}: {
  label: string;
  detail: string;
  pct: number | null;
  onClick?: () => void;
}) {
  const tone =
    pct === null ? null : pct < 60 ? "var(--success)" : pct < 85 ? "var(--warning)" : "var(--danger)";
  const body = (
    <>
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-xs font-medium text-[var(--text-secondary)]">{label}</span>
        {onClick && <span className="text-xs text-[var(--text-tertiary)] opacity-0 transition-opacity group-hover:opacity-100">7-day history</span>}
      </div>
      <div className="mt-1 text-xl font-semibold tabular-nums text-[var(--text-primary)]">
        {pct === null ? "—" : `${pct}%`}
      </div>
      <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-[var(--bg-tertiary)]">
        <div
          className="h-full rounded-full transition-[width]"
          style={{ width: pct === null ? "0%" : `${Math.max(pct, 2)}%`, background: tone ?? undefined }}
        />
      </div>
      <div className="mt-1.5 truncate text-xs tabular-nums text-[var(--text-tertiary)]">{detail}</div>
    </>
  );
  const cls = "group bg-[var(--bg-primary)] px-3 py-2.5 text-left";
  if (!onClick) return <div className={cls}>{body}</div>;
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={`${label}: ${pct === null ? "no usage data" : `${pct}% used`}, ${detail}. Show 7-day history`}
      className={cn(
        cls,
        "transition-colors outline-none hover:bg-[var(--bg-secondary)] focus-visible:ring-2 focus-visible:ring-[var(--focus)] focus-visible:ring-inset"
      )}
    >
      {body}
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

interface PinnedVolume {
  namespace: string;
  pvc: string;
  kind: "addon" | "volume" | "other";
  project?: string;
  owner?: string;
  size?: string;
}

// UncordonButton makes a cordoned node schedulable again.
function UncordonButton({ node }: { node: string }) {
  const qc = useQueryClient();
  const uncordon = useMutation({
    mutationFn: () =>
      api(`/api/kubernetes/nodes/${encodeURIComponent(node)}/uncordon`, { method: "POST" }),
    onSuccess: () => {
      toast.success(`${node} is schedulable again`);
      qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    },
    onError: (e) => toastError(e, "Uncordon failed"),
  });
  return (
    <Button size="xs" variant="secondary" onClick={() => uncordon.mutate()} loading={uncordon.isPending}>
      Uncordon
    </Button>
  );
}

// RemoveNodeButton: cordon → drain → delete, behind a typed-name
// confirm. Data on node-local volumes is listed first; removing anyway
// sends force=true.
function RemoveNodeButton({ node }: { node: NodeSummary }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const preflight = useQuery({
    queryKey: ["kubernetes", "nodes", node.name, "remove-preflight"],
    queryFn: () =>
      api<{ pinned: PinnedVolume[] }>(
        `/api/kubernetes/nodes/${encodeURIComponent(node.name)}/remove-preflight`,
      ).then((r) => r.pinned ?? []),
    enabled: open,
    staleTime: 0,
  });
  const pinned = preflight.data ?? [];
  const remove = useMutation({
    mutationFn: () =>
      api(
        `/api/kubernetes/nodes/${encodeURIComponent(node.name)}/remove${pinned.length > 0 ? "?force=true" : ""}`,
        { method: "POST", body: { force: false } },
      ),
    onSuccess: () => {
      toast.success(`Removed ${node.name}`);
      setOpen(false);
      qc.invalidateQueries({ queryKey: ["kubernetes", "nodes"] });
    },
    onError: (err) => {
      toastError(err, "Remove failed");
    },
  });
  return (
    <>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setOpen(true)}
        aria-label={`Remove ${node.name}`}
        className="-mr-1.5 text-[var(--text-tertiary)] hover:bg-[var(--error-subtle)] hover:text-[var(--error)]"
      >
        <Trash2 aria-hidden />
        <span className="hidden sm:inline">Remove</span>
      </Button>
      <ConfirmDialog
        open={open}
        title={`Remove ${node.name}?`}
        body={
          <div className="space-y-2">
            <p>Workloads move to other nodes, then the node leaves the cluster.</p>
            {preflight.isPending ? (
              <p className="text-xs text-[var(--text-tertiary)]">Checking for data on this node…</p>
            ) : preflight.isError ? (
              <p className="text-xs text-[var(--warning)]">
                Couldn&apos;t check for data on this node:{" "}
                {preflight.error instanceof Error ? preflight.error.message : "unknown error"}
              </p>
            ) : pinned.length > 0 ? (
              <div className="rounded-md border border-[var(--error)]/30 bg-[var(--error-subtle)] p-2">
                <p className="text-xs font-medium text-[var(--error)]">
                  This data lives only on {node.name} and will be lost:
                </p>
                <ul className="mt-1 space-y-0.5 font-mono text-xs">
                  {pinned.map((p) => (
                    <li key={`${p.namespace}/${p.pvc}`} className="flex gap-2">
                      <span className="truncate">
                        {p.project ? `${p.project} · ` : ""}
                        {p.owner || p.pvc}
                      </span>
                      <span className="text-[var(--text-tertiary)]">{p.kind}</span>
                      {p.size && <span className="ml-auto text-[var(--text-tertiary)]">{p.size}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}
          </div>
        }
        typeToConfirm={node.name}
        confirmLabel={pinned.length > 0 ? "Remove and lose data" : "Remove node"}
        pending={remove.isPending || preflight.isPending}
        onConfirm={() => remove.mutate()}
        onCancel={() => setOpen(false)}
      />
    </>
  );
}

// SSHKey is the wire shape returned by /api/ssh-keys. Private bytes
// never leave the server; we only see the public half + fingerprint.
interface SSHKey {
  id: string;
  name: string;
  publicKey: string;
  fingerprint: string;
  createdAt: string;
}

// AddNodeModal — two paths to add a worker node:
//
//   Bootstrap (default, v0.10+):  the operator copies a curl one-liner and
//     pastes it on the new VM. Single-use 15-min token; the VM detects
//     facts (arch, cloud, instance-type), redeems the token, runs the
//     standard k3s install. Works behind NAT, no SSH config from kuso.
//
//   SSH (legacy):  the operator pastes their SSH public key on the VM,
//     kuso opens an SSH session and runs the install. Still useful when
//     the VM can't reach kuso outbound but kuso can reach it inbound
//     (e.g. office LAN with firewall asymmetry).
//
// The Bootstrap path is the default because it requires zero VM
// preparation past `curl + sh`. SSH stays available behind a tab.
type AddNodeMode = "bootstrap" | "ssh";

function AddNodeModal({
  onClose,
  onJoined,
}: {
  onClose: () => void;
  onJoined: () => void;
}) {
  const [mode, setMode] = useState<AddNodeMode>("bootstrap");
  const qc = useQueryClient();
  const canDeleteKeys = useCan(Perms.SettingsAdmin);
  const keys = useQuery({
    queryKey: ["ssh-keys"],
    queryFn: () => api<SSHKey[]>("/api/ssh-keys"),
  });
  const [host, setHost] = useState("");
  const [port, setPort] = useState("22");
  const [user, setUser] = useState("root");
  const [keyId, setKeyId] = useState<string>("");
  const [region, setRegion] = useState("");
  const [output, setOutput] = useState<string | null>(null);
  const [validation, setValidation] = useState<{
    ok: boolean;
    checks: { label: string; ok: boolean; detail?: string }[];
  } | null>(null);
  const [showNewKey, setShowNewKey] = useState(false);
  const [confirmKeyDelete, setConfirmKeyDelete] = useState(false);

  const selectedKey = (keys.data ?? []).find((k) => k.id === keyId);

  // Inline two-step confirm rather than a ConfirmDialog: this modal's own
  // window Escape handler would also close it underneath a nested dialog.
  const deleteKey = useMutation({
    meta: { skipGlobalErrorToast: true },
    mutationFn: (id: string) => api<void>(`/api/ssh-keys/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: (_, id) => {
      toast.success("SSH key deleted");
      if (keyId === id) setKeyId("");
      setValidation(null);
      void qc.invalidateQueries({ queryKey: ["ssh-keys"] });
    },
    onError: (err) => toastError(err, "Delete failed"),
    onSettled: () => setConfirmKeyDelete(false),
  });

  const validate = useMutation({
    mutationFn: () =>
      api<{ ok: boolean; checks: { label: string; ok: boolean; detail?: string }[] }>(
        "/api/kubernetes/nodes/validate",
        {
          method: "POST",
          body: {
            host: host.trim(),
            port: Number(port) || 22,
            user: user.trim() || "root",
            sshKeyId: keyId,
          },
        }
      ),
    onSuccess: (res) => {
      setValidation(res);
      if (res.ok) toast.success("Validation passed — ready to join");
      else toast.error("Validation has errors — see the checks below");
    },
    onError: (err) => {
      toastError(err, "Validate failed");
    },
  });

  const join = useMutation({
    mutationFn: () => {
      const labels: Record<string, string> = {};
      if (region.trim()) labels[`region`] = region.trim();
      return api<{ output: string; nodeName: string }>("/api/kubernetes/nodes/join", {
        method: "POST",
        body: {
          host: host.trim(),
          port: Number(port) || 22,
          user: user.trim() || "root",
          sshKeyId: keyId,
          labels,
        },
      });
    },
    onSuccess: (data) => {
      setOutput(data.output);
      toast.success(`Node ${data.nodeName || host} joined`);
      onJoined();
    },
    onError: (err) => {
      // A remote-side failure (502) carries the install log in `output`.
      const body = err instanceof ApiError ? err.body : null;
      const remoteOut =
        body && typeof body === "object" && "output" in body && typeof body.output === "string"
          ? body.output
          : "";
      const msg = err instanceof Error ? err.message : String(err);
      setOutput(remoteOut ? `${remoteOut}\n\n${msg}` : msg);
      toast.error("Join failed — see install output");
    },
  });

  const validateDisabled = !host.trim() || !keyId || validate.isPending;
  // Allow Join even when validation hasn't run, but encourage the
  // happy path by enabling the button only after a successful
  // validation. Users who insist on skipping can re-click Validate
  // until it passes — the cheap UX safeguard, not a hard block.
  const joinDisabled = !host.trim() || !keyId || join.isPending || !validation?.ok;

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !join.isPending && !validate.isPending) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose, join.isPending, validate.isPending]);

  const busy = join.isPending || validate.isPending;

  return (
    <div
      role="dialog"
      aria-modal="true"
      onClick={() => !busy && onClose()}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-6"
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="w-full max-w-xl max-h-[90vh] overflow-y-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] shadow-[var(--shadow-lg)]"
      >
        <header className="flex items-center justify-between border-b border-[var(--border-subtle)] px-4 py-3">
          <div>
            <h2 className="text-sm font-medium">Add node</h2>
            <p className="text-xs text-[var(--text-tertiary)]">
              {mode === "bootstrap"
                ? "Paste one command on the new VM"
                : "k3s agent join: validate, then install"}
            </p>
          </div>
          <button
            type="button"
            disabled={busy}
            onClick={onClose}
            aria-label="Close"
            className="rounded p-1 text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)] disabled:opacity-40"
          >
            <X className="h-4 w-4" />
          </button>
        </header>
        {/* Tab strip — Bootstrap is the recommended path; SSH is
            kept for legacy / firewall-asymmetric environments. */}
        <div className="flex border-b border-[var(--border-subtle)] px-4 pt-3">
          {[
            { id: "bootstrap" as const, label: "Bootstrap", subtitle: "Recommended" },
            { id: "ssh" as const, label: "SSH", subtitle: "Legacy" },
          ].map((tab) => (
            <button
              key={tab.id}
              type="button"
              disabled={busy}
              onClick={() => setMode(tab.id)}
              className={cn(
                "mr-1 -mb-px min-h-10 border-b-2 px-3 py-2 text-[13px] font-medium transition disabled:opacity-40",
                mode === tab.id
                  ? "border-[var(--accent)] text-[var(--text-primary)]"
                  : "border-transparent text-[var(--text-tertiary)] hover:text-[var(--text-secondary)]",
              )}
            >
              {tab.label}{" "}
              <span className="text-xs text-[var(--text-tertiary)]">· {tab.subtitle}</span>
            </button>
          ))}
        </div>
        {mode === "bootstrap" && (
          <BootstrapBody
            onClose={onClose}
            onJoined={onJoined}
            busy={busy}
          />
        )}
        {mode === "ssh" && (
        <div className="space-y-3 p-4">
          {/* Step 1: SSH key. Either pick from the library or
              generate a fresh ed25519 keypair on the server. */}
          <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
            <p className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
              SSH key
            </p>
            {keys.isPending ? (
              <Skeleton className="h-8 w-full" />
            ) : (
              <SettingsSelect
                value={keyId}
                onChange={(e) => {
                  setKeyId(e.target.value);
                  setValidation(null);
                  setConfirmKeyDelete(false);
                }}
                className="font-mono text-xs"
              >
                <option value="">Pick a key</option>
                {(keys.data ?? []).map((k) => (
                  <option key={k.id} value={k.id}>
                    {k.name} ({k.fingerprint.slice(0, 24)}…)
                  </option>
                ))}
              </SettingsSelect>
            )}
            {selectedKey && (
              <div className="mt-2 space-y-1.5">
                <p className="text-xs text-[var(--text-tertiary)]">
                  Paste this public key into the new VM&apos;s{" "}
                  <span className="font-mono">~/.ssh/authorized_keys</span>:
                </p>
                <code className="block max-h-24 overflow-auto rounded bg-[var(--bg-tertiary)] p-2 font-mono text-xs break-all">
                  {selectedKey.publicKey}
                </code>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    void navigator.clipboard.writeText(selectedKey.publicKey);
                    toast.success("Public key copied");
                  }}
                >
                  Copy public key
                </Button>
                {confirmKeyDelete ? (
                  <div className="flex flex-wrap items-center gap-2 rounded border border-[var(--error)]/30 bg-[var(--error-subtle)] px-2 py-1.5">
                    <span className="flex-1 text-xs text-[var(--error)]">
                      Delete {selectedKey.name}? Joined nodes keep running; the private key is gone from kuso for good.
                    </span>
                    <Button size="sm" variant="ghost" onClick={() => setConfirmKeyDelete(false)} disabled={deleteKey.isPending}>
                      Cancel
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => deleteKey.mutate(selectedKey.id)}
                      disabled={deleteKey.isPending}
                    >
                      {deleteKey.isPending ? "Deleting…" : "Delete key"}
                    </Button>
                  </div>
                ) : (
                  canDeleteKeys && (
                    <Button size="sm" variant="ghost" onClick={() => setConfirmKeyDelete(true)}>
                      <Trash2 className="h-3 w-3" /> Delete key
                    </Button>
                  )
                )}
              </div>
            )}
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setShowNewKey((v) => !v)}
              className="mt-2"
            >
              {showNewKey ? "Cancel" : "+ Generate new key"}
            </Button>
            {showNewKey && (
              <NewKeyForm
                onCreated={(k) => {
                  qc.invalidateQueries({ queryKey: ["ssh-keys"] });
                  setKeyId(k.id);
                  setShowNewKey(false);
                  toast.success(`Key ${k.name} generated — copy the public key above`);
                }}
              />
            )}
          </section>

          {/* Step 2: Host details. */}
          <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
            <p className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
              Target VM
            </p>
            <div className="grid grid-cols-3 gap-2">
              <div className="col-span-2 space-y-1">
                <label className="text-xs text-[var(--text-tertiary)]">Host / IP</label>
                <Input
                  value={host}
                  onChange={(e) => {
                    setHost(e.target.value);
                    setValidation(null);
                  }}
                  placeholder="10.0.0.5 or worker-1.example.com"
                  className="h-8 text-[13px]"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-[var(--text-tertiary)]">Port</label>
                <Input
                  value={port}
                  onChange={(e) => {
                    setPort(e.target.value);
                    setValidation(null);
                  }}
                  className="h-8 text-[13px]"
                />
              </div>
            </div>
            <div className="mt-2 grid grid-cols-2 gap-2">
              <div className="space-y-1">
                <label className="text-xs text-[var(--text-tertiary)]">SSH user</label>
                <Input
                  value={user}
                  onChange={(e) => {
                    setUser(e.target.value);
                    setValidation(null);
                  }}
                  className="h-8 text-[13px]"
                />
              </div>
              <div className="space-y-1">
                <label className="text-xs text-[var(--text-tertiary)]">
                  Region label (optional)
                </label>
                <Input
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  placeholder="eu"
                  className="h-8 text-[13px]"
                />
              </div>
            </div>
          </section>

          {/* Step 3: Validation results. */}
          {validation && (
            <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
              <p className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
                Validation
              </p>
              <ul className="space-y-1">
                {validation.checks.map((c, i) => (
                  <li key={i} className="flex items-start gap-2 font-mono text-xs">
                    <span
                      className={cn(
                        "mt-0.5 inline-block h-1.5 w-1.5 shrink-0 rounded-full",
                        c.ok ? "bg-[var(--success)]" : "bg-[var(--danger)]"
                      )}
                      role="img"
                      aria-label={c.ok ? "passed" : "failed"}
                    />
                    <span className="w-24 shrink-0 text-[var(--text-secondary)]">{c.label}</span>
                    <span
                      className={cn(c.ok ? "text-[var(--text-secondary)]" : "text-[var(--error)]")}
                    >
                      {c.detail}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {output && (
            <pre className="max-h-48 overflow-auto rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-xs text-[var(--text-secondary)]">
              {output}
            </pre>
          )}
        </div>
        )}
        {mode === "ssh" && (
        <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
          <Button size="sm" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={validateDisabled}
            onClick={() => validate.mutate()}
          >
            {validate.isPending ? "Validating…" : "Validate"}
          </Button>
          <Button size="sm" disabled={joinDisabled} onClick={() => join.mutate()}>
            {join.isPending ? "Joining…" : "Join"}
          </Button>
        </footer>
        )}
      </div>
    </div>
  );
}

// BootstrapBody — pull-mode add-node UX. Default tab in AddNodeModal.
//
// UX, top to bottom:
//
//   1. Optional region/labels/name input. None of it is required;
//      everything has sensible defaults so a fresh user can press
//      "Generate command" without thinking.
//   2. The curl one-liner with a Copy button + a tooltip showing the
//      token's TTL. The same one-liner runs on every distro.
//   3. A live status line that flips from "waiting for the node to
//      call home…" to "joined as <name>" once the agent registers,
//      backed by the pending-tokens poll.
function BootstrapBody({
  onClose,
  onJoined,
  busy,
}: {
  onClose: () => void;
  onJoined: () => void;
  busy: boolean;
}) {
  const qc = useQueryClient();
  const [region, setRegion] = useState("");
  const [tier, setTier] = useState("");
  const [nodeName, setNodeName] = useState("");
  const [minted, setMinted] = useState<MintedTokenLite | null>(null);

  // Pending-tokens poll. We only enable it once a token has been
  // minted; before that, the poll would just hammer the API for an
  // empty list. 5s is fast enough that the operator sees the status
  // flip almost in real time, slow enough that 10 admins minting in
  // parallel still produces under 1 RPS to the endpoint.
  const pending = useQuery({
    queryKey: ["node-bootstrap", "pending"],
    queryFn: () =>
      api<{ tokens: PendingTokenLite[] }>("/api/kubernetes/nodes/bootstrap-tokens"),
    enabled: !!minted,
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
  });

  // Watch the pending list — if our prefix drops out of it, the token
  // was consumed (the new VM phoned home). Match by jtiPrefix because
  // the cleartext jti is one-shot and the server only returns hash
  // prefixes in the list response from now on.
  const stillPending = useMemo(
    () => (minted ? pending.data?.tokens.some((t) => t.jtiPrefix === minted.jtiPrefix) : false),
    [minted, pending.data],
  );
  // The shared pending cache still holds the pre-mint list right after a
  // mint, so "absent" only means redeemed once we've seen the token
  // listed. onJoined is read through a ref (callers pass an inline arrow)
  // and the toast fires once per token.
  const seenPendingRef = useRef<string | null>(null);
  const handledRef = useRef<string | null>(null);
  const onJoinedRef = useRef(onJoined);
  useEffect(() => {
    onJoinedRef.current = onJoined;
  });
  useEffect(() => {
    if (!minted || !pending.isSuccess) return;
    if (stillPending) {
      seenPendingRef.current = minted.jtiPrefix;
      return;
    }
    if (handledRef.current === minted.jtiPrefix) return;
    // Dropping out of the pending list also happens on expiry or a
    // revoke from the pending-tokens card, not only on redemption.
    if (new Date(minted.expiresAt).getTime() <= Date.now()) {
      handledRef.current = minted.jtiPrefix;
      toast.warning("Bootstrap token expired before a node used it — generate a new command");
      setMinted(null);
      return;
    }
    if (seenPendingRef.current !== minted.jtiPrefix) return;
    handledRef.current = minted.jtiPrefix;
    toast.success("Token redeemed — the node should appear in the list shortly");
    onJoinedRef.current();
    // Don't auto-close; the operator may want to verify the install
    // log scrolled successfully on the VM. They click Done.
  }, [minted, pending.isSuccess, stillPending]);

  const mint = useMutation({
    mutationFn: () => {
      const labels: Record<string, string> = {};
      if (region.trim()) labels["region"] = region.trim();
      if (tier.trim()) labels["tier"] = tier.trim();
      return api<MintedTokenLite>("/api/kubernetes/nodes/bootstrap-tokens", {
        method: "POST",
        body: {
          labels,
          nodeName: nodeName.trim() || undefined,
        },
      });
    },
    onSuccess: (data) => {
      setMinted(data);
      void qc.invalidateQueries({ queryKey: ["node-bootstrap", "pending"] });
    },
    onError: (err) => {
      toastError(err, "Mint failed");
    },
  });

  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const revoke = useMutation({
    mutationFn: (jti: string) =>
      api<void>(`/api/kubernetes/nodes/bootstrap-tokens/${encodeURIComponent(jti)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      setConfirmRevoke(false);
      setMinted(null);
      void qc.invalidateQueries({ queryKey: ["node-bootstrap", "pending"] });
      toast.success("Token revoked");
    },
  });

  return (
    <>
      <div className="space-y-3 p-4">
        {!minted && (
          <>
            <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
              <p className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
                Optional metadata
              </p>
              <div className="grid grid-cols-2 gap-2">
                <div className="space-y-1">
                  <label className="text-xs text-[var(--text-tertiary)]">
                    Region
                  </label>
                  <Input
                    value={region}
                    onChange={(e) => setRegion(e.target.value)}
                    placeholder="eu"
                    className="h-8 text-[13px]"
                  />
                </div>
                <div className="space-y-1">
                  <label className="text-xs text-[var(--text-tertiary)]">
                    Tier
                  </label>
                  <Input
                    value={tier}
                    onChange={(e) => setTier(e.target.value)}
                    placeholder="premium"
                    className="h-8 text-[13px]"
                  />
                </div>
              </div>
              <div className="mt-2 space-y-1">
                <label className="text-xs text-[var(--text-tertiary)]">
                  Node name override (optional)
                </label>
                <Input
                  value={nodeName}
                  onChange={(e) => setNodeName(e.target.value)}
                  placeholder="defaults to VM hostname"
                  className="h-8 text-[13px]"
                />
              </div>
            </section>
            <p className="text-xs text-[var(--text-secondary)]">
              You&apos;ll get a one-line curl command. Paste it as root on the new VM —
              it detects facts (arch, cloud, instance type), redeems a single-use
              token, and runs the standard k3s agent install. Token expires in 15
              minutes.
            </p>
          </>
        )}

        {minted && (
          <>
            <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
              <p className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
                Run this on the new VM
              </p>
              <code className="block max-h-32 overflow-auto rounded bg-[var(--bg-tertiary)] p-2 font-mono text-xs break-all text-[var(--text-primary)]">
                {minted.oneLiner}
              </code>
              <div className="mt-2 flex items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    void navigator.clipboard.writeText(minted.oneLiner);
                    toast.success("Command copied");
                  }}
                >
                  Copy command
                </Button>
                <span className="text-xs text-[var(--text-tertiary)]">
                  Expires {new Date(minted.expiresAt).toLocaleTimeString()}
                </span>
              </div>
            </section>
            <section className="rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-3">
              <p className="mb-1 text-xs text-[var(--text-secondary)] font-medium">
                Status
              </p>
              <div className="flex items-center gap-2 font-mono text-xs">
                {stillPending ? (
                  <>
                    <span className="inline-block h-2 w-2 animate-pulse rounded-full bg-[var(--warning)]" />
                    <span className="text-[var(--text-secondary)]">
                      Waiting for the new VM to call home…
                    </span>
                  </>
                ) : pending.isSuccess ? (
                  <>
                    <span className="inline-block h-2 w-2 rounded-full bg-[var(--success)]" />
                    <span className="text-[var(--text-secondary)]">
                      Node phoned home — joining the cluster
                    </span>
                  </>
                ) : (
                  <span className="text-[var(--text-tertiary)]">Checking…</span>
                )}
              </div>
            </section>
          </>
        )}
      </div>
      <footer className="flex items-center justify-end gap-2 border-t border-[var(--border-subtle)] px-4 py-3">
        {!minted && (
          <>
            <Button size="sm" variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button
              size="sm"
              disabled={mint.isPending}
              onClick={() => mint.mutate()}
            >
              {mint.isPending ? "Minting…" : "Generate command"}
            </Button>
          </>
        )}
        {minted && (
          <>
            <Button
              size="sm"
              variant="ghost"
              disabled={revoke.isPending}
              onClick={() => setConfirmRevoke(true)}
            >
              {revoke.isPending ? "Revoking…" : "Revoke token"}
            </Button>
            <Button size="sm" onClick={onClose}>
              Done
            </Button>
          </>
        )}
      </footer>
      <ConfirmDialog
        open={confirmRevoke}
        title="Revoke this bootstrap token?"
        body={
          <p>
            The join command stops working. The token can&apos;t be recovered; you&apos;d
            have to generate a new command.
          </p>
        }
        confirmLabel="Revoke token"
        destructive
        pending={revoke.isPending}
        onConfirm={() => minted && revoke.mutate(minted.jtiPrefix)}
        onCancel={() => setConfirmRevoke(false)}
      />
    </>
  );
}

// Local types for the bootstrap response shape. Mirrors the server's
// JSON. We re-declare here rather than importing to keep this file
// self-contained for the existing-code edit; the canonical types live
// in features/node-bootstrap/api.ts.
type MintedTokenLite = {
  jti: string;
  jtiPrefix: string;
  expiresAt: string;
  oneLiner: string;
  labels?: Record<string, string>;
  nodeName?: string;
};

type PendingTokenLite = {
  jtiPrefix: string;
  jtiHash: string;
  createdAt: string;
  expiresAt: string;
  labels?: Record<string, string>;
  nodeName?: string;
  createdBy?: string;
  oneLiner: string;
};

// NewKeyForm submits to /api/ssh-keys to generate (or paste) a new
// keypair. Keeps the inline UX tight — one name field + a generate
// switch. Pasting is reachable via "show advanced" if the operator
// already has a key they want to reuse.
function NewKeyForm({ onCreated }: { onCreated: (k: SSHKey) => void }) {
  const [name, setName] = useState("");
  const [advanced, setAdvanced] = useState(false);
  const [publicKey, setPublicKey] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const create = useMutation({
    mutationFn: () =>
      api<SSHKey>("/api/ssh-keys", {
        method: "POST",
        body: advanced
          ? { name, generate: false, publicKey, privateKey }
          : { name, generate: true },
      }),
    onSuccess: (k) => onCreated(k),
    onError: (e) => toastError(e, "Key create failed"),
  });
  return (
    <div className="mt-2 space-y-2 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-secondary)] p-2">
      <Input
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="key name (e.g. workers-eu)"
        className="h-7 text-xs"
      />
      <button
        type="button"
        onClick={() => setAdvanced((v) => !v)}
        className="text-xs text-[var(--text-tertiary)] hover:text-[var(--text-primary)]"
      >
        {advanced ? "Generate an ed25519 key instead" : "Paste an existing key instead"}
      </button>
      {advanced && (
        <>
          <textarea
            value={publicKey}
            onChange={(e) => setPublicKey(e.target.value)}
            placeholder="ssh-ed25519 AAAA… user@host"
            spellCheck={false}
            rows={2}
            className="block w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-xs"
          />
          <textarea
            value={privateKey}
            onChange={(e) => setPrivateKey(e.target.value)}
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;…"
            spellCheck={false}
            rows={4}
            className="block w-full rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] p-2 font-mono text-xs"
          />
        </>
      )}
      <Button
        size="sm"
        disabled={!name.trim() || create.isPending || (advanced && (!publicKey.trim() || !privateKey.trim()))}
        onClick={() => create.mutate()}
      >
        {create.isPending ? "Creating…" : advanced ? "Save key" : "Generate"}
      </Button>
    </div>
  );
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
            <h2 className="text-sm font-medium">{node.name}</h2>
            <p className="text-xs text-[var(--text-tertiary)]">
              {title} · last 7 days · sampled every 5 min
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
            <p className="text-xs text-[var(--error)]">
              Failed to load history: {history.error?.message}
            </p>
          ) : samples.length === 0 ? (
            <p className="text-xs text-[var(--text-tertiary)]">
              No samples yet. The kuso server samples nodes every 5 min — check back shortly after
              a fresh deploy.
            </p>
          ) : (
            <Sparkline metric={metric} samples={samples} node={node} />
          )}
          <TopConsumers node={node} metric={metric} />
        </div>
      </div>
    </div>
  );
}

interface Consumer {
  kind: "service" | "addon" | "cron" | "build" | "platform" | "system" | "host";
  project?: string;
  name: string;
  namespace: string;
  pods: number;
  value: number;
}
interface ResourceConsumers {
  used: number;
  consumers: Consumer[];
  unattributed: number;
}
interface ConsumersResponse {
  node: string;
  at: string;
  consumers: {
    cpu: ResourceConsumers;
    memory: ResourceConsumers;
    disk: ResourceConsumers & { capacity: number; images: number };
  };
  volumes: PinnedVolume[];
}

const TOP_CONSUMERS = 8;

const UNATTRIBUTED_LABEL: Record<NodeMetricKind, { label: string; hint: string }> = {
  cpu: { label: "system", hint: "kernel and other host processes" },
  mem: { label: "system", hint: "kernel and other host processes" },
  disk: { label: "volumes, logs & OS", hint: "local-path volume data, system logs and the OS" },
};

// TopConsumers answers "what is using it" under the history chart: a
// live snapshot from the node's kubelet, grouped by kuso service/addon
// (or workload for non-kuso pods). Disk adds the image store and lists
// the volumes pinned to the node — local-path volumes can't be sized
// by the kubelet, so they are shown by allocated size, not usage.
function TopConsumers({ node, metric }: { node: NodeSummary; metric: NodeMetricKind }) {
  const q = useQuery({
    queryKey: ["kubernetes", "nodes", node.name, "consumers"],
    queryFn: () =>
      api<ConsumersResponse>(`/api/kubernetes/nodes/${encodeURIComponent(node.name)}/consumers`),
    refetchInterval: 10_000,
    staleTime: 5_000,
  });

  const fmt = metric === "cpu" ? formatCpu : formatBytes;
  let section: ResourceConsumers | undefined;
  let capacity = 0;
  if (q.data) {
    const c = q.data.consumers;
    if (metric === "cpu") {
      section = c.cpu;
      capacity = node.cpuCapacityMilli ?? 0;
    } else if (metric === "mem") {
      section = c.memory;
      capacity = node.memCapacityBytes ?? 0;
    } else {
      section = c.disk;
      capacity = c.disk.capacity;
    }
  }

  return (
    <section className="mt-4 border-t border-[var(--border-subtle)] pt-3">
      <h3 className="mb-2 text-xs text-[var(--text-secondary)] font-medium">
        Top consumers · now
      </h3>
      {q.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : q.isError ? (
        <p className="text-xs text-[var(--error)]">
          {q.error instanceof ApiError ? q.error.message : "Failed to load consumers"}
        </p>
      ) : section ? (
        <ConsumerList
          section={section}
          capacity={capacity}
          fmt={fmt}
          metric={metric}
          images={metric === "disk" ? q.data.consumers.disk.images : 0}
        />
      ) : null}
      {metric === "disk" && q.data && q.data.volumes.length > 0 && (
        <PinnedVolumes volumes={q.data.volumes} />
      )}
    </section>
  );
}

function ConsumerList({
  section,
  capacity,
  fmt,
  metric,
  images,
}: {
  section: ResourceConsumers;
  capacity: number;
  fmt: (n: number) => string;
  metric: NodeMetricKind;
  images: number;
}) {
  const top = section.consumers.slice(0, TOP_CONSUMERS);
  const rest = section.consumers.slice(TOP_CONSUMERS);
  const restValue = rest.reduce((sum, c) => sum + c.value, 0);
  const other = UNATTRIBUTED_LABEL[metric];
  const rows: { key: string; kind: string; label: string; detail?: string; value: number }[] = [];
  if (images > 0) {
    rows.push({ key: "images", kind: "images", label: "container images", value: images });
  }
  for (const c of top) {
    rows.push({
      key: `${c.kind}/${c.namespace}/${c.project ?? ""}/${c.name}`,
      kind: c.kind,
      label: c.project ? `${c.project} / ${c.name}` : c.name,
      detail: consumerDetail(c),
      value: c.value,
    });
  }
  if (rest.length > 0) {
    rows.push({ key: "rest", kind: "", label: `${rest.length} more`, value: restValue });
  }
  if (section.unattributed > 0) {
    rows.push({ key: "other", kind: "", label: other.label, detail: other.hint, value: section.unattributed });
  }
  rows.sort((a, b) => b.value - a.value);

  if (rows.length === 0) {
    return <p className="text-xs text-[var(--text-tertiary)]">No usage reported yet.</p>;
  }
  const max = rows[0].value || 1;
  return (
    <ul className="space-y-1.5">
      {rows.map((r) => {
        const pct = capacity > 0 ? Math.round((r.value / capacity) * 100) : null;
        return (
          <li key={r.key} className="font-mono text-xs">
            <div className="flex items-baseline justify-between gap-3">
              <div className="flex min-w-0 items-baseline gap-2">
                {r.kind && (
                  <span className="shrink-0 rounded bg-[var(--bg-tertiary)] px-1 text-2xs text-[var(--text-secondary)] font-medium">
                    {r.kind}
                  </span>
                )}
                <span className="truncate text-[var(--text-primary)]" title={r.label}>
                  {r.label}
                </span>
                {r.detail && (
                  <span className="hidden truncate text-xs text-[var(--text-tertiary)] sm:inline" title={r.detail}>
                    {r.detail}
                  </span>
                )}
              </div>
              <span className="shrink-0 tabular-nums text-[var(--text-secondary)]">
                {fmt(r.value)}
                {pct !== null && <span className="ml-1 text-[var(--text-tertiary)]">({pct}%)</span>}
              </span>
            </div>
            <div className="mt-0.5 h-0.5 w-full overflow-hidden rounded bg-[var(--bg-tertiary)]">
              <div className="h-full bg-[var(--success)]/70" style={{ width: `${(r.value / max) * 100}%` }} />
            </div>
          </li>
        );
      })}
    </ul>
  );
}

function consumerDetail(c: Consumer): string | undefined {
  if (c.kind === "host") return "node process, outside pods";
  const pods = `${c.pods} pod${c.pods === 1 ? "" : "s"}`;
  return c.project ? pods : `${c.namespace} · ${pods}`;
}

function PinnedVolumes({ volumes }: { volumes: PinnedVolume[] }) {
  return (
    <div className="mt-4">
      <h3 className="text-xs text-[var(--text-secondary)] font-medium">
        Volumes on this disk · allocated size
      </h3>
      <p className="mb-2 text-xs text-[var(--text-tertiary)]">
        local-path volumes can&apos;t be measured individually; their data is part of &quot;volumes, logs &amp; OS&quot; above.
      </p>
      <ul className="grid grid-cols-1 gap-x-4 gap-y-0.5 sm:grid-cols-2">
        {volumes.map((v) => (
          <li key={`${v.namespace}/${v.pvc}`} className="flex justify-between gap-2 font-mono text-xs">
            <span className="truncate text-[var(--text-secondary)]" title={`${v.namespace}/${v.pvc}`}>
              {v.project && v.owner ? `${v.project} / ${v.owner}` : v.pvc}
            </span>
            <span className="shrink-0 text-[var(--text-tertiary)]">{v.size ?? "—"}</span>
          </li>
        ))}
      </ul>
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
  node,
}: {
  metric: NodeMetricKind;
  samples: HistorySample[];
  // The live point-in-time numbers off /api/kubernetes/nodes. Used
  // to render a "now" marker at the right edge of the chart so the
  // user can see the spike that hasn't been sampled into SQLite yet.
  // Without this, a spike at minute 0 of a 5-minute sample window
  // shows up only after the next tick — confusing when the inline
  // tile already reads 86%.
  node: NodeSummary;
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

  // Live "now" point, computed off the same NodeSummary the inline
  // tile reads. Even when the latest sample is up to 5 min stale,
  // the marker stays in sync with what the user sees on the card.
  const liveNow = (() => {
    let used = 0;
    let cap = 0;
    if (metric === "cpu") {
      used = node.cpuUsageMilli ?? 0;
      cap = node.cpuCapacityMilli ?? 0;
    } else if (metric === "mem") {
      used = node.memUsageBytes ?? 0;
      cap = node.memCapacityBytes ?? 0;
    } else {
      cap = node.diskCapacityBytes ?? 0;
      used = cap - (node.diskAvailableBytes ?? 0);
    }
    if (cap <= 0 || used <= 0) return null;
    return { ts: Date.now(), pct: Math.min(100, (used / cap) * 100), used, cap };
  })();

  const tMin = points[0]?.ts ?? 0;
  // Stretch the right edge to "now" so the live marker has a place
  // to live without falling outside the chart. When the latest
  // sample is recent (< 1 min), tMax stays at the sample so we
  // don't add a sliver of empty space.
  const lastSampleTs = points[points.length - 1]?.ts ?? tMin + 1;
  const tMax =
    liveNow && liveNow.ts > lastSampleTs ? liveNow.ts : lastSampleTs;
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
      ? formatCpu(latest?.used ?? 0)
      : formatBytes(latest?.used ?? 0);
  const latestCap =
    metric === "cpu"
      ? formatCpu(latest?.cap ?? 0)
      : formatBytes(latest?.cap ?? 0);

  // Format helpers reused for the live row.
  const fmtUsedCap = (p: { used: number; cap: number; pct: number }) => {
    const u = metric === "cpu" ? formatCpu(p.used) : formatBytes(p.used);
    const c = metric === "cpu" ? formatCpu(p.cap) : formatBytes(p.cap);
    return `${u} / ${c} (${Math.round(p.pct)}%)`;
  };

  return (
    <div>
      <div className="mb-2 grid grid-cols-2 gap-2 font-mono text-xs">
        <div className="flex items-baseline justify-between rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1">
          <span className="font-sans text-[var(--text-tertiary)]">Last sample</span>
          <span>
            <span className="text-[var(--text-primary)]">{latestUsed}</span>{" "}
            <span className="text-[var(--text-tertiary)]">
              / {latestCap} ({latest ? Math.round(latest.pct) : 0}%)
            </span>
          </span>
        </div>
        <div className="flex items-baseline justify-between rounded-md border border-[var(--border-subtle)] bg-[var(--bg-primary)] px-2 py-1">
          <span className="text-[var(--text-tertiary)]">live now</span>
          <span>
            {liveNow ? (
              <span className="text-[var(--text-primary)]">{fmtUsedCap(liveNow)}</span>
            ) : (
              <span className="text-[var(--text-tertiary)]">—</span>
            )}
          </span>
        </div>
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
              className="fill-[var(--text-tertiary)] font-mono text-xs"
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
        {/* Live marker — bridges the gap between the most recent
            sample and "now" with a dotted segment, plus a filled
            circle at the right edge. The line color stays the same
            (matches latest pressure tier), but the dotted style + ring
            make it obvious the right tip is live, not sampled. */}
        {liveNow && points.length > 0 && (
          <>
            <line
              x1={x(points[points.length - 1].ts)}
              y1={y(points[points.length - 1].pct)}
              x2={x(liveNow.ts)}
              y2={y(liveNow.pct)}
              stroke={stroke}
              strokeWidth={1.5}
              strokeDasharray="3 2"
              opacity={0.7}
            />
            <circle
              cx={x(liveNow.ts)}
              cy={y(liveNow.pct)}
              r={3.5}
              fill={stroke}
              stroke="var(--bg-secondary)"
              strokeWidth={1.5}
            />
          </>
        )}
        {xLabels.map((p, i) => (
          <text
            key={`${p.ts}-${i}`}
            x={x(p.ts)}
            y={H - 6}
            textAnchor={i === 0 ? "start" : i === xLabels.length - 1 ? "end" : "middle"}
            className="fill-[var(--text-tertiary)] font-mono text-xs"
          >
            {fmtTs(p.ts)}
          </text>
        ))}
      </svg>
      <p className="mt-2 text-xs text-[var(--text-tertiary)]">
        {points.length} sample{points.length === 1 ? "" : "s"}
      </p>
    </div>
  );
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

// PendingBootstrapTokensCard surfaces minted-but-unredeemed bootstrap
// tokens. Hidden when empty so the page stays tidy on a healthy
// install. Each row carries a Revoke button that flips the token's
// revokedAt — operators use this when they pasted the wrong command
// into the wrong VM, or just decided not to add the node after all.
function PendingBootstrapTokensCard() {
  const qc = useQueryClient();
  const pending = useQuery({
    queryKey: ["node-bootstrap", "pending"],
    queryFn: () =>
      api<{ tokens: PendingTokenRow[] }>("/api/kubernetes/nodes/bootstrap-tokens"),
    refetchInterval: 10_000,
    refetchIntervalInBackground: false,
  });
  const revoke = useMutation({
    mutationFn: (jti: string) =>
      api<void>(`/api/kubernetes/nodes/bootstrap-tokens/${encodeURIComponent(jti)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      setRevoking(null);
      void qc.invalidateQueries({ queryKey: ["node-bootstrap", "pending"] });
      toast.success("Token revoked");
    },
    onError: (e) => toastError(e, "Revoke failed"),
  });
  const [revoking, setRevoking] = useState<PendingTokenRow | null>(null);
  const tokens = pending.data?.tokens ?? [];
  if (tokens.length === 0) return null;
  return (
    <section className="rounded-md border border-[var(--warning)]/30 bg-[var(--warning-subtle)] p-3">
      <p className="mb-2 text-xs text-[var(--warning)] font-medium">
        Pending bootstrap tokens · {tokens.length}
      </p>
      <ul className="space-y-1.5">
        {tokens.map((t) => (
          <li
            key={t.jtiHash}
            className="flex flex-wrap items-center justify-between gap-2 rounded bg-[var(--bg-primary)] p-2 font-mono text-xs"
          >
            <div className="min-w-0 flex-1 space-y-0.5">
              <div className="truncate text-[var(--text-secondary)]">
                <span className="font-sans text-[var(--text-tertiary)]">Prefix</span>{" "}
                <span>{t.jtiPrefix}…</span>
                {t.nodeName && (
                  <>
                    <span className="ml-3 font-sans text-[var(--text-tertiary)]">Name</span>{" "}
                    <span>{t.nodeName}</span>
                  </>
                )}
              </div>
              <div className="text-xs text-[var(--text-tertiary)]">
                Expires {new Date(t.expiresAt).toLocaleTimeString()}
                {t.labels && Object.keys(t.labels).length > 0 && (
                  <>
                    {" · "}
                    {Object.entries(t.labels)
                      .map(([k, v]) => `${k}=${v}`)
                      .join(", ")}
                  </>
                )}
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              {/* No "Copy" button: the cleartext token is one-shot at
                  mint and the server only returns hash prefixes here.
                  An operator who lost the one-liner must revoke + re-mint. */}
              <Button
                size="sm"
                variant="ghost"
                disabled={revoke.isPending}
                onClick={() => setRevoking(t)}
              >
                Revoke
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <ConfirmDialog
        open={revoking !== null}
        title={`Revoke bootstrap token ${revoking?.jtiPrefix ?? ""}…?`}
        body={
          <p>
            The join command using this token stops working. The token can&apos;t be
            recovered; you&apos;d have to generate a new command.
          </p>
        }
        confirmLabel="Revoke token"
        destructive
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.jtiHash)}
        onCancel={() => setRevoking(null)}
      />
    </section>
  );
}

type PendingTokenRow = {
  jtiPrefix: string;
  jtiHash: string;
  createdAt: string;
  expiresAt: string;
  labels?: Record<string, string>;
  nodeName?: string;
  createdBy?: string;
};
