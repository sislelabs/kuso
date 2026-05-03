"use client";

import { useEffect, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  Panel,
  type Node,
  type Edge,
  type NodeMouseHandler,
  type OnNodesChange,
  applyNodeChanges,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";

import type { KusoAddon, KusoEnvironment, KusoService } from "@/types/projects";
import { ServiceNode, type ServiceNodeData } from "./ServiceNode";
import { AddonNode, type AddonNodeData } from "./AddonNode";
import {
  applyStoredLayout,
  autoLayout,
  loadStoredLayout,
  saveStoredLayout,
} from "./layout";
import { CanvasContextMenu, type ContextMenuItem } from "./CanvasContextMenu";
import { AddAddonDialog } from "@/components/addon/AddAddonDialog";
import { ConfirmDialog } from "@/components/shared/ConfirmDialog";
import { serviceShortName } from "@/lib/utils";
import { useTriggerBuild } from "@/features/services";
import {
  ExternalLink,
  ScrollText,
  RotateCcw,
  Trash2,
  Eye,
  Plus,
  LayoutGrid,
} from "lucide-react";
import { toast } from "sonner";

const nodeTypes = {
  service: ServiceNode,
  addon: AddonNode,
};

interface Props {
  project: string;
  services: KusoService[];
  addons: KusoAddon[];
  envs: KusoEnvironment[];
  onSelectService?: (svcName: string) => void;
  onSelectAddon?: (addonName: string) => void;
}

interface ContextState {
  open: boolean;
  x: number;
  y: number;
  items: ContextMenuItem[];
}

export function ProjectCanvas({
  project,
  services,
  addons,
  envs,
  onSelectService,
  onSelectAddon,
}: Props) {
  const initialNodes: Node[] = useMemo(() => {
    const out: Node[] = [];
    services.forEach((s) => {
      const env = envs.find(
        (e) => e.spec.service === s.metadata.name && e.spec.kind === "production"
      );
      out.push({
        id: `svc:${s.metadata.name}`,
        type: "service",
        position: { x: 0, y: 0 },
        data: { project, service: s, env } satisfies ServiceNodeData,
      });
    });
    addons.forEach((a) => {
      out.push({
        id: `addon:${a.metadata.name}`,
        type: "addon",
        position: { x: 0, y: 0 },
        data: { project, addon: a } satisfies AddonNodeData,
      });
    });
    return out;
  }, [project, services, addons, envs]);

  const initialEdges: Edge[] = useMemo(() => {
    const out: Edge[] = [];
    // Addon → service: only when the service explicitly references
    // the addon's conn-secret. The kuso server auto-attaches every
    // addon's secret to every service env via envFromSecrets, but
    // that means "secret is mounted on the pod" — NOT "service uses
    // it." A service that never reads DATABASE_URL shouldn't have
    // a line drawn to postgres on the canvas.
    //
    // We detect explicit references in two forms:
    //   1. valueFrom.secretKeyRef.name == "<addon>-conn"
    //      (set by the server when it resolves `${{addon.KEY}}`)
    //   2. value contains the still-unresolved `${{addon.KEY}}` text
    //      (rare — only on freshly typed env vars before save round-trip)
    const usedAddons = new Set<string>(); // "<service-fqn>|<addon-fqn>"
    services.forEach((s) => {
      for (const ev of s.spec.envVars ?? []) {
        // valueFrom path: server-resolved addon refs land here.
        const skr = (ev?.valueFrom as { secretKeyRef?: { name?: string } } | undefined)?.secretKeyRef;
        if (skr?.name && skr.name.endsWith("-conn")) {
          // Conn-secret naming is "<addon-cr-name>-conn" — strip the
          // suffix and match against known addons.
          const addonFQN = skr.name.slice(0, -"-conn".length);
          if (addons.some((a) => a.metadata.name === addonFQN)) {
            usedAddons.add(`${s.metadata.name}|${addonFQN}`);
          }
        }
        // value path: literal ${{addon.KEY}} text. Server resolves
        // these on save, but during the brief window before the next
        // refetch the canvas would drop the edge — so we still draw
        // it.
        if (ev?.value) {
          const m = ev.value.match(/^\$\{\{\s*([a-zA-Z0-9_-]+)\.[A-Z_][A-Z0-9_]*\s*\}\}$/);
          if (m) {
            // Check both short + fqn names against known addons.
            const refName = m[1];
            const candidates = [refName, `${project}-${refName}`];
            for (const c of candidates) {
              if (addons.some((a) => a.metadata.name === c)) {
                usedAddons.add(`${s.metadata.name}|${c}`);
                break;
              }
            }
          }
        }
      }
    });
    usedAddons.forEach((key) => {
      const [svcFQN, addonFQN] = key.split("|");
      out.push({
        id: `e:${addonFQN}->${svcFQN}`,
        source: `addon:${addonFQN}`,
        target: `svc:${svcFQN}`,
        animated: true,
        // The `kind` field is non-standard React Flow data; we
        // stash the edge category here so the filter chips can
        // toggle visibility without re-deriving from the id.
        // Colour: amber. Distinct from service refs (the accent
        // colour) so the two categories read as separate at a
        // glance.
        data: { kind: "addon" },
        style: { stroke: "rgb(245 158 11)", strokeWidth: 1.5, opacity: 0.85 },
      });
    });
    // Service → service edges from env-var refs. The server resolves
    // `${{ x.URL }}` to a literal in-cluster DNS string at SetEnv
    // time, so by the time the canvas reads spec.envVars the ref is
    // already gone. We reverse it: any value that points at another
    // service's cluster-local DNS counts as a dep.
    //
    // Pattern: <fqn>.<ns>.svc.cluster.local — fqn is the kube Service
    // name, which is the same as the KusoService CR name.
    // Dedupe service→service edges. Without this, a service that
    // references another via three env vars (API_URL, API_HOST,
    // API_PORT) renders as three parallel lines + label spam. One
    // edge per (source, target) pair is the right model — the
    // "direct link" is the dependency, not each individual var.
    const seenRefEdges = new Set<string>();
    services.forEach((s) => {
      const ownFqn = s.metadata.name;
      for (const ev of s.spec.envVars ?? []) {
        if (!ev?.value) continue;
        // Match the FQN in the value. The ref forms HOST/URL/etc.
        // all start with "<fqn>.<ns>.svc.cluster.local" so this
        // single regex covers all four.
        const m = ev.value.match(/([a-z0-9-]+)\.[a-z0-9-]+\.svc\.cluster\.local/);
        if (!m) continue;
        const targetFqn = m[1];
        if (targetFqn === ownFqn) continue;
        if (!services.some((t) => t.metadata.name === targetFqn)) continue;
        const edgeKey = `${targetFqn}->${ownFqn}`;
        if (seenRefEdges.has(edgeKey)) continue;
        seenRefEdges.add(edgeKey);
        out.push({
          id: `eref:${edgeKey}`,
          source: `svc:${targetFqn}`,
          target: `svc:${ownFqn}`,
          animated: true,
          data: { kind: "ref" },
          // Colour: accent (purple). Service-ref edges are direct
          // dependencies via in-cluster DNS — distinct from addon
          // mounts (amber).
          style: { stroke: "var(--accent)", strokeWidth: 1.5, opacity: 0.85 },
        });
      }
    });
    return out;
  }, [project, services, addons]);

  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>(initialEdges);
  const [ctx, setCtx] = useState<ContextState>({ open: false, x: 0, y: 0, items: [] });
  // Add-addon dialog. Lives next to ctx because both are short-lived
  // canvas-scoped overlays — no point hoisting up to the page view.
  const [showAddAddon, setShowAddAddon] = useState(false);
  // Pending delete-confirm. Set when the user picks "Delete service"
  // from the right-click menu; the ConfirmDialog renders below; on
  // confirm we run the API + optimistically yank the node out of
  // state so the canvas feels instant. Replaces the old window.confirm
  // pattern which was both ugly and slow (modal-blocked event loop +
  // wait for refetch before the node disappears).
  const [confirmDelete, setConfirmDelete] = useState<{
    kind: "service" | "addon";
    short: string;
    nodeId: string;
  } | null>(null);
  const [deleting, setDeleting] = useState(false);

  // Edge category visibility. The two kinds today:
  //   - addon: the project's addon-conn Secret is mounted on every
  //            service via envFrom. Real direct dep, not "indirect."
  //   - ref:   service references another service via env-var refs
  //            like ${{api.URL}} → ends up as a literal DNS string.
  // Defaults to both ON; toggling lets you de-clutter dense projects.
  const [edgeFilters, setEdgeFilters] = useState<{ addon: boolean; ref: boolean }>({
    addon: true,
    ref: true,
  });

  const trigger = useTriggerBuild(project, "");

  useEffect(() => {
    if (!project) return;
    const stored = loadStoredLayout(project);
    const laid = autoLayout(initialNodes, initialEdges);
    setNodes(applyStoredLayout(laid, stored));
    setEdges(initialEdges);
  }, [project, initialNodes, initialEdges]);

  const onNodesChange: OnNodesChange = (changes) => {
    setNodes((prev) => {
      const next = applyNodeChanges(changes, prev);
      // Persist on any position change. We used to gate on
      // `dragging === false`, but React Flow emits position changes
      // where `dragging` is undefined (e.g. programmatic moves, drop
      // events on some browsers), and those slipped through silently
      // — the user moved a node, the state updated, but localStorage
      // never got the write. Save unconditionally on type=position;
      // the cost is one localStorage.setItem per drag tick, which is
      // negligible for a graph of this size.
      const positionChanged = changes.some((c) => c.type === "position");
      if (positionChanged) {
        const layout: Record<string, { x: number; y: number }> = {};
        next.forEach((n) => {
          layout[n.id] = n.position;
        });
        saveStoredLayout(project, layout);
      }
      return next;
    });
  };

  const onNodeClick: NodeMouseHandler = (_e, node) => {
    if (node.type === "service" && onSelectService) {
      const data = node.data as ServiceNodeData;
      onSelectService(serviceShortName(data.project, data.service.metadata.name));
    } else if (node.type === "addon" && onSelectAddon) {
      const data = node.data as AddonNodeData;
      onSelectAddon(data.addon.metadata.name);
    }
  };

  // Right-click on a service node — Open / View logs / Trigger build / Delete.
  const onServiceContext = (
    e: React.MouseEvent,
    data: ServiceNodeData
  ) => {
    e.preventDefault();
    const short = serviceShortName(data.project, data.service.metadata.name);
    const env = envs.find(
      (x) =>
        x.spec.service === data.service.metadata.name && x.spec.kind === "production"
    );
    const url = env?.status?.url as string | undefined;

    const items: ContextMenuItem[] = [
      {
        id: "open",
        label: "Open service",
        icon: Eye,
        onSelect: () => onSelectService?.(short),
      },
      {
        id: "logs",
        label: "View logs",
        icon: ScrollText,
        onSelect: () => onSelectService?.(short),
      },
      {
        id: "trigger",
        label: "Trigger build",
        icon: RotateCcw,
        onSelect: async () => {
          try {
            await callTrigger(data.project, short, trigger);
            toast.success(`Build triggered for ${short}`);
          } catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to trigger build");
          }
        },
      },
      ...(url
        ? [
            {
              id: "open-url",
              label: "Open URL in new tab",
              icon: ExternalLink,
              onSelect: () => window.open(url, "_blank", "noopener"),
            } as ContextMenuItem,
          ]
        : []),
      {
        id: "delete",
        label: "Delete service…",
        icon: Trash2,
        destructive: true,
        onSelect: () =>
          setConfirmDelete({
            kind: "service",
            short,
            nodeId: `svc:${data.service.metadata.name}`,
          }),
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  // Right-click on an addon node — Open / Delete. Delete sends the
  // user into the overlay's Settings tab where the typed-name
  // confirm gate lives; we don't trust window.confirm for a
  // destructive op that may take a PVC with it.
  const onAddonContext = (e: React.MouseEvent, data: AddonNodeData) => {
    e.preventDefault();
    const items: ContextMenuItem[] = [
      {
        id: "open",
        label: "Open addon",
        icon: Eye,
        onSelect: () => onSelectAddon?.(data.addon.metadata.name),
      },
      {
        id: "delete",
        label: "Delete addon…",
        icon: Trash2,
        destructive: true,
        onSelect: () =>
          setConfirmDelete({
            kind: "addon",
            short: data.addon.metadata.name,
            nodeId: `addon:${data.addon.metadata.name}`,
          }),
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  // Right-click on empty pane — Add service / Add addon / Reset layout.
  const onPaneContext = (e: React.MouseEvent) => {
    e.preventDefault();
    const items: ContextMenuItem[] = [
      {
        id: "add-service",
        label: "Add service",
        icon: Plus,
        onSelect: () => {
          window.location.href = `/projects/${encodeURIComponent(project)}/services/new`;
        },
      },
      {
        id: "add-addon",
        label: "Add addon",
        icon: Plus,
        onSelect: () => setShowAddAddon(true),
      },
      {
        id: "reset-layout",
        label: "Reset layout",
        icon: LayoutGrid,
        onSelect: () => {
          saveStoredLayout(project, {});
          const laid = autoLayout(initialNodes, initialEdges);
          setNodes(laid);
        },
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  return (
    <div
      className="relative flex-1 min-h-0 w-full"
      onContextMenuCapture={(e) => {
        // React Flow's nodeTypes render their own divs; if a node was
        // right-clicked the event already had its propagation stopped
        // by our node-level handler. Anything else that bubbles up
        // here is a pane context and we route it accordingly.
        const target = e.target as HTMLElement;
        if (target.closest("[data-node-context]")) return;
        onPaneContext(e);
      }}
    >
      <ReactFlow
        nodes={nodes.map((n) => ({
          ...n,
          data: {
            ...n.data,
            __onContext:
              n.type === "service"
                ? (e: React.MouseEvent) => onServiceContext(e, n.data as ServiceNodeData)
                : (e: React.MouseEvent) => onAddonContext(e, n.data as AddonNodeData),
          },
        }))}
        edges={edges.filter((e) => {
          // Hide categories the user toggled off. Edges without a
          // kind tag (legacy or future categories) are always shown
          // so an upgrade can't accidentally swallow them.
          const kind = (e.data as { kind?: string } | undefined)?.kind;
          if (kind === "addon" && !edgeFilters.addon) return false;
          if (kind === "ref" && !edgeFilters.ref) return false;
          return true;
        })}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.25, maxZoom: 1, minZoom: 0.4 }}
        minZoom={0.2}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
      >
        {/* Brighter dots than the default border-subtle so the
            grid actually reads on the dark bg. zinc-500 at 40%
            opacity-equivalent — visible without being noisy. */}
        <Background gap={24} size={1.5} color="rgb(113 113 122 / 0.55)" />
        <Controls className="!bg-[var(--bg-elevated)] !border-[var(--border-subtle)]" />
        <EdgeFilterPanel filters={edgeFilters} setFilters={setEdgeFilters} />
      </ReactFlow>

      <CanvasContextMenu
        open={ctx.open}
        x={ctx.x}
        y={ctx.y}
        items={ctx.items}
        onClose={() => setCtx((c) => ({ ...c, open: false }))}
      />

      <AddAddonDialog
        project={project}
        open={showAddAddon}
        onClose={() => setShowAddAddon(false)}
      />

      <ConfirmDialog
        open={!!confirmDelete}
        title={
          confirmDelete?.kind === "service"
            ? `Delete service ${confirmDelete?.short}`
            : `Delete addon ${confirmDelete?.short}`
        }
        body={
          confirmDelete?.kind === "service" ? (
            <>
              This <strong>cascades</strong> to every environment of{" "}
              <span className="font-mono text-[var(--text-primary)]">
                {confirmDelete?.short}
              </span>{" "}
              and tears down running pods. The git repo is untouched.
            </>
          ) : (
            <>
              Drops the helm release for{" "}
              <span className="font-mono text-[var(--text-primary)]">
                {confirmDelete?.short}
              </span>
              . The PVC + data go with it unless your storage class retains it.
            </>
          )
        }
        typeToConfirm={confirmDelete?.short}
        confirmLabel={
          confirmDelete?.kind === "service" ? "Delete service" : "Delete addon"
        }
        pending={deleting}
        onCancel={() => setConfirmDelete(null)}
        onConfirm={async () => {
          if (!confirmDelete) return;
          const { short, nodeId, kind } = confirmDelete;
          setDeleting(true);

          // Optimistic: pull the node out of state immediately. The
          // refetch will confirm; if the API call fails we restore.
          // Keep a snapshot of the previous state for rollback.
          const prevNodes = nodes;
          setNodes((cur) => cur.filter((n) => n.id !== nodeId));
          // Close the modal on the same tick so the canvas renders
          // the removal without the user staring at a "Working…"
          // button.
          setConfirmDelete(null);

          try {
            if (kind === "service") {
              const { deleteService } = await import("@/features/services/api");
              await deleteService(project, short);
            } else {
              const { deleteAddon } = await import("@/features/projects/api");
              await deleteAddon(project, short);
            }
            toast.success(
              kind === "service"
                ? `Service ${short} deleted`
                : `Addon ${short} deleted`
            );
          } catch (err) {
            // Restore the node so the canvas matches reality.
            setNodes(prevNodes);
            toast.error(err instanceof Error ? err.message : "Failed to delete");
          } finally {
            setDeleting(false);
          }
        }}
      />
    </div>
  );
}

// callTrigger adapter: the trigger hook is bound to a placeholder
// service name (""), so the canvas re-binds to the actual service
// via the API directly. Keeps the canvas from spinning up N hooks
// per node.
async function callTrigger(
  project: string,
  service: string,
  _hint: ReturnType<typeof useTriggerBuild>
) {
  void _hint;
  const { triggerBuild } = await import("@/features/services/api");
  await triggerBuild(project, service, {});
}

// EdgeFilterPanel — bottom-left chip group that toggles edge
// categories on the canvas. Lives as a React Flow <Panel> so it
// floats over the graph the same way Controls does.
function EdgeFilterPanel({
  filters,
  setFilters,
}: {
  filters: { addon: boolean; ref: boolean };
  setFilters: React.Dispatch<React.SetStateAction<{ addon: boolean; ref: boolean }>>;
}) {
  return (
    <Panel position="bottom-right" className="!m-3">
      <div className="flex items-center gap-1 rounded-md border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-1 shadow-[var(--shadow-sm)]">
        <span className="px-2 font-mono text-[10px] uppercase tracking-widest text-[var(--text-tertiary)]">
          edges
        </span>
        <FilterChip
          label="addon mounts"
          on={filters.addon}
          onClick={() => setFilters((f) => ({ ...f, addon: !f.addon }))}
        />
        <FilterChip
          label="service refs"
          on={filters.ref}
          onClick={() => setFilters((f) => ({ ...f, ref: !f.ref }))}
        />
      </div>
    </Panel>
  );
}

function FilterChip({
  label,
  on,
  onClick,
}: {
  label: string;
  on: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        "rounded px-2 py-1 font-mono text-[10px] transition-colors " +
        (on
          ? "bg-[var(--accent-subtle)] text-[var(--accent)]"
          : "text-[var(--text-tertiary)] hover:bg-[var(--bg-tertiary)] hover:text-[var(--text-primary)]")
      }
    >
      {label}
    </button>
  );
}
