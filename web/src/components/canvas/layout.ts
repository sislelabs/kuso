// Initial canvas layout via dagre. Computes (x, y) for each node so
// services stack on the left and addons on the right; edges flow
// left-to-right. Layout runs once on first load — drags persist to
// localStorage from then on.

import dagre from "dagre";
import type { Node, Edge } from "@xyflow/react";
import { serviceShortName } from "@/lib/utils";
import { serviceNodeWidth } from "./geometry";

const NODE_WIDTH = 280;
const NODE_HEIGHT = 140;

// Service cards widen for long names; lay them out at the width they render.
function widthOf(n: Node): number {
  if (n.type !== "service") return NODE_WIDTH;
  const d = n.data as { project?: unknown; service?: { metadata?: { name?: unknown }; spec?: { displayName?: unknown } } };
  const display = typeof d.service?.spec?.displayName === "string" ? d.service.spec.displayName.trim() : "";
  const full = typeof d.service?.metadata?.name === "string" ? d.service.metadata.name : "";
  return serviceNodeWidth(display || serviceShortName(typeof d.project === "string" ? d.project : "", full));
}

export function autoLayout(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 32, ranksep: 96 });
  g.setDefaultEdgeLabel(() => ({}));

  // dagre orders nodes within a rank by insertion order, and the API
  // returns services/addons in no fixed order: sort so a first load
  // without a stored layout lands the same way every time.
  const byId = (a: { id: string }, b: { id: string }) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  [...nodes].sort(byId).forEach((n) => {
    g.setNode(n.id, { width: widthOf(n), height: NODE_HEIGHT });
  });
  [...edges].sort(byId).forEach((e) => {
    g.setEdge(e.source, e.target);
  });

  dagre.layout(g);

  return nodes.map((n) => {
    const pos = g.node(n.id);
    return {
      ...n,
      position: {
        x: pos.x - widthOf(n) / 2,
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
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return {};
    const out: StoredLayout = {};
    for (const [id, pos] of Object.entries(parsed as Record<string, unknown>)) {
      const p = pos as { x?: unknown; y?: unknown } | null;
      if (p && typeof p.x === "number" && typeof p.y === "number" && Number.isFinite(p.x) && Number.isFinite(p.y)) {
        out[id] = { x: p.x, y: p.y };
      }
    }
    return out;
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

export function clearStoredLayout(project: string) {
  if (typeof window === "undefined") return;
  try {
    localStorage.removeItem(STORAGE_KEY(project));
  } catch {
    // storage disabled; nothing to clear
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
