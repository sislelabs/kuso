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
import { serviceShortName } from "@/lib/utils";

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
    // Connect every addon to every service in the project — addon
    // connection secrets are wired into every service via envFrom, so
    // the canvas reflects that "broadcast" relationship.
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

  // Layout pass: dagre then overlay any user-saved positions.
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
      // Persist positions on drag end (each ChangeType "position" with
      // dragging:false marks a settled position).
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
      // Pass the SHORT name (without the project prefix) so the URL
      // hosting the overlay (?service=<short>) matches what the API
      // expects for /api/projects/:p/services/:s.
      onSelectService(serviceShortName(data.project, data.service.metadata.name));
    } else if (node.type === "addon" && onSelectAddon) {
      const data = node.data as AddonNodeData;
      onSelectAddon(data.addon.metadata.name);
    }
  };

  return (
    // Parent (project view) controls our height: it's a flex-1 region
    // inside a column that starts below the global Header + toolbar.
    // Use flex-1 + min-h-0 so we fill that without overflowing. No
    // explicit calc(100vh - ...) here; that math is brittle when the
    // toolbar height changes.
    <div className="flex-1 min-h-0 w-full">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeClick={onNodeClick}
        fitView
        // Cap initial zoom: with one or two nodes, fitView would scale
        // them up to maxZoom (which used to be 2x) and the canvas
        // looked like a magnifier. 1.0 = "actual size".
        fitViewOptions={{ padding: 0.25, maxZoom: 1, minZoom: 0.4 }}
        minZoom={0.2}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={24} size={1} color="var(--border-subtle)" />
        <Controls className="!bg-[var(--bg-elevated)] !border-[var(--border-subtle)]" />
      </ReactFlow>
    </div>
  );
}
