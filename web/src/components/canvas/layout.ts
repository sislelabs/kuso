// Initial canvas layout via dagre. Computes (x, y) for each node so
// services stack on the left and addons on the right; edges flow
// left-to-right. Layout runs once on first load — drags persist to
// localStorage from then on.

import dagre from "dagre";
import type { Node, Edge } from "@xyflow/react";

const NODE_WIDTH = 280;
const NODE_HEIGHT = 140;

export function autoLayout(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 32, ranksep: 96 });
  g.setDefaultEdgeLabel(() => ({}));

  nodes.forEach((n) => {
    g.setNode(n.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  });
  edges.forEach((e) => {
    g.setEdge(e.source, e.target);
  });

  dagre.layout(g);

  return nodes.map((n) => {
    const pos = g.node(n.id);
    return {
      ...n,
      position: {
        x: pos.x - NODE_WIDTH / 2,
        y: pos.y - NODE_HEIGHT / 2,
      },
    };
  });
}

const STORAGE_KEY = (project: string) => `kuso.canvas.layout.${project}`;

export type StoredLayout = Record<string, { x: number; y: number }>;

export function loadStoredLayout(project: string): StoredLayout {
  if (typeof window === "undefined") return {};
  try {
    const raw = localStorage.getItem(STORAGE_KEY(project));
    if (!raw) return {};
    return JSON.parse(raw) as StoredLayout;
  } catch {
    return {};
  }
}

export function saveStoredLayout(project: string, layout: StoredLayout) {
  if (typeof window === "undefined") return;
  try {
    localStorage.setItem(STORAGE_KEY(project), JSON.stringify(layout));
  } catch {
    // localStorage might be full or disabled; skip silently
  }
}

export function applyStoredLayout(nodes: Node[], stored: StoredLayout): Node[] {
  return nodes.map((n) => {
    if (stored[n.id]) {
      return { ...n, position: stored[n.id] };
    }
    return n;
  });
}
