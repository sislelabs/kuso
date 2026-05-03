"use client";

import { useEffect, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
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
import { serviceShortName } from "@/lib/utils";
import { useTriggerBuild, useDeleteService } from "@/features/services";
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
    addons.forEach((a) => {
      services.forEach((s) => {
        out.push({
          id: `e:${a.metadata.name}->${s.metadata.name}`,
          source: `addon:${a.metadata.name}`,
          target: `svc:${s.metadata.name}`,
          animated: true,
          style: { stroke: "var(--accent)", strokeWidth: 1.5, opacity: 0.5 },
        });
      });
    });
    return out;
  }, [services, addons]);

  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>(initialEdges);
  const [ctx, setCtx] = useState<ContextState>({ open: false, x: 0, y: 0, items: [] });

  const trigger = useTriggerBuild(project, "");
  const del = useDeleteService(project, "");

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
      const dragged = changes.some(
        (c) => c.type === "position" && c.dragging === false
      );
      if (dragged) {
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
        label: "Delete service",
        icon: Trash2,
        destructive: true,
        onSelect: async () => {
          if (!window.confirm(`Delete service "${short}"? This cascades to its environments.`)) return;
          try {
            await callDelete(data.project, short, del);
            toast.success(`Service ${short} deleted`);
          } catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to delete");
          }
        },
      },
    ];
    setCtx({ open: true, x: e.clientX, y: e.clientY, items });
  };

  // Right-click on an addon node — Open / Connection / Delete.
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
        label: "Delete addon",
        icon: Trash2,
        destructive: true,
        disabled: true,
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
          window.location.href = `/projects/${encodeURIComponent(project)}/settings`;
        },
      },
      {
        id: "add-addon",
        label: "Add addon",
        icon: Plus,
        disabled: true,
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
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.25, maxZoom: 1, minZoom: 0.4 }}
        minZoom={0.2}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={24} size={1} color="var(--border-subtle)" />
        <Controls className="!bg-[var(--bg-elevated)] !border-[var(--border-subtle)]" />
      </ReactFlow>

      <CanvasContextMenu
        open={ctx.open}
        x={ctx.x}
        y={ctx.y}
        items={ctx.items}
        onClose={() => setCtx((c) => ({ ...c, open: false }))}
      />
    </div>
  );
}

// callTrigger / callDelete are tiny adapters: the hooks at the top of
// this module are bound to a placeholder service name (""), so we
// re-bind to the actual service via the API directly using the same
// mutation infra. Keeps the canvas from spinning up N hooks per node.
async function callTrigger(
  project: string,
  service: string,
  _hint: ReturnType<typeof useTriggerBuild>
) {
  void _hint;
  const { triggerBuild } = await import("@/features/services/api");
  await triggerBuild(project, service, {});
}

async function callDelete(
  project: string,
  service: string,
  _hint: ReturnType<typeof useDeleteService>
) {
  void _hint;
  const { deleteService } = await import("@/features/services/api");
  await deleteService(project, service);
}
