"use client";

import { Handle, Position } from "@xyflow/react";
import type { Side } from "./geometry";

const SIDES: { id: Side; position: Position }[] = [
  { id: "top", position: Position.Top },
  { id: "right", position: Position.Right },
  { id: "bottom", position: Position.Bottom },
  { id: "left", position: Position.Left },
];

// One handle per side. The canvas runs in loose connection mode, so each
// handle is both an edge anchor (edges pick the sides facing each other)
// and a drag source for wiring a new connection. They only show while the
// node is hovered or focused so idle graphs aren't dotted with them.
export function NodeHandles() {
  return (
    <>
      {SIDES.map((s) => (
        <Handle
          key={s.id}
          id={s.id}
          type="source"
          position={s.position}
          className="!h-2 !w-2 !border-[var(--bg-elevated)] !bg-[var(--accent)] !opacity-0 transition-opacity group-hover:!opacity-100 group-focus-within:!opacity-100"
        />
      ))}
    </>
  );
}
