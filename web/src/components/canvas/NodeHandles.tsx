"use client";

import { Handle, Position } from "@xyflow/react";
import { PORT_SLOTS, portId, slotFraction, type Side } from "./geometry";

const SIDES: { id: Side; position: Position }[] = [
  { id: "top", position: Position.Top },
  { id: "right", position: Position.Right },
  { id: "bottom", position: Position.Bottom },
  { id: "left", position: Position.Left },
];

const MIDDLE = (PORT_SLOTS - 1) / 2;

// PORT_SLOTS anchors per side (see assignPorts), so edges meeting one side
// fan out instead of stacking on its midpoint. The canvas runs in loose
// connection mode: the middle anchor of each side doubles as the drag
// source for wiring a new connection and shows while the node is hovered
// or focused; the others are invisible edge anchors only.
export function NodeHandles() {
  return (
    <>
      {SIDES.flatMap((s) =>
        Array.from({ length: PORT_SLOTS }, (_, i) => {
          const pct = `${slotFraction(i) * 100}%`;
          const vertical = s.id === "left" || s.id === "right";
          const grip = i === MIDDLE;
          return (
            <Handle
              key={portId(s.id, i)}
              id={portId(s.id, i)}
              type="source"
              position={s.position}
              isConnectable={grip}
              style={vertical ? { top: pct } : { left: pct }}
              className={
                grip
                  ? "!h-2 !w-2 !border-[var(--bg-elevated)] !bg-[var(--accent)] !opacity-0 transition-opacity group-hover:!opacity-100 group-focus-within:!opacity-100"
                  : "!pointer-events-none !h-px !w-px !min-h-0 !min-w-0 !border-0 !bg-transparent !opacity-0"
              }
            />
          );
        }),
      )}
    </>
  );
}
