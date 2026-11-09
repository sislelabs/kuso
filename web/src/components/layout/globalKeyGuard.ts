// What the global keyboard layer needs from a key event's target and the
// document; plain shapes so the guard is testable without a DOM.
export interface KeyTarget {
  closest(selector: string): unknown;
}
export interface ModalQuery {
  querySelector(selector: string): unknown;
}

// A base-ui popup that is animating out carries data-closed /
// data-ending-style while it is still in the DOM, with focus still inside.
const CLOSING_LAYER = "[data-closed], [data-ending-style]";
const OPEN_MODAL = "[role=dialog][data-open], [role=alertdialog]:not([data-closed])";

// globalKeyBlocked says whether a global shortcut key must be left alone:
// it was typed into a field, or another modal (palette, confirm) is open.
// A field inside a dialog that is already closing doesn't count: Esc on
// the palette then "?" straight away used to land in the palette's input
// during its exit animation and was swallowed.
export function globalKeyBlocked(
  target: KeyTarget | null,
  typing: boolean,
  helpOpen: boolean,
  doc: ModalQuery,
): boolean {
  const closing = !!target?.closest(CLOSING_LAYER);
  if (typing && !closing) return true;
  return !helpOpen && !!doc.querySelector(OPEN_MODAL);
}
