// Fields ProjectCanvas injects into every node's data for the roving
// tab stop: exactly one node has tabIndex 0, the rest -1, and arrow
// keys move between them (see geometry.nextInDirection).
export interface CanvasNodeA11y extends Record<string, unknown> {
  __tabIndex?: 0 | -1;
}

export const nodeFocusClass =
  "outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-[var(--focus)]";
