import { describe, expect, it } from "vitest";
import type { Edge, Node } from "@xyflow/react";
import { autoLayout } from "./layout";

const node = (id: string): Node => ({ id, position: { x: 0, y: 0 }, data: {} });
const edge = (source: string, target: string): Edge => ({ id: `${source}->${target}`, source, target });

describe("autoLayout", () => {
  it("places nodes the same way whatever order they arrive in", () => {
    const nodes = ["svc:p-web", "svc:p-api", "svc:p-worker", "addon:p-db", "addon:p-cache"].map(node);
    const edges = [edge("addon:p-db", "svc:p-api"), edge("addon:p-cache", "svc:p-web"), edge("addon:p-db", "svc:p-worker")];
    const pos = (ns: Node[]) => Object.fromEntries(ns.map((n) => [n.id, n.position]));

    const a = pos(autoLayout(nodes, edges));
    const b = pos(autoLayout([...nodes].reverse(), [...edges].reverse()));
    expect(b).toEqual(a);
  });
});
