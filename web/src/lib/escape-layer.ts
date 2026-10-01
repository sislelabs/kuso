// Escape routing between stacked surfaces.
//
// Overlays (ServiceOverlay, AddonOverlay) and the modals/menus opened
// inside them all listen for Escape on `window`. Window listeners run in
// registration order, so the overlay — mounted first — would otherwise
// close underneath a ConfirmDialog and drop the user's unsaved edits.
//
// Nested surfaces listen in the capture phase and call claimEscape();
// outer surfaces bail when escapeHandledAbove() says someone above them
// owns this keypress.

export const NESTED_SURFACE_SELECTOR =
  '[aria-modal="true"], [data-slot="dialog-content"], [data-slot="popover-content"], [data-escape-layer]';

export function claimEscape(e: Pick<KeyboardEvent, "preventDefault">): void {
  e.preventDefault();
}

interface Surface {
  contains(other: Surface | null): boolean;
}

function queryNested(): Surface[] {
  if (typeof document === "undefined") return [];
  return Array.from(document.querySelectorAll(NESTED_SURFACE_SELECTOR));
}

// escapeHandledAbove reports whether an Escape keypress belongs to a
// surface stacked above `root`: already claimed, or some other modal /
// popover / menu is open that isn't `root` itself or one of its ancestors.
export function escapeHandledAbove(
  e: Pick<KeyboardEvent, "defaultPrevented">,
  root: Surface | null,
  nested: Surface[] = queryNested(),
): boolean {
  if (e.defaultPrevented) return true;
  return nested.some((el) => el !== root && !(root && el.contains(root)));
}

// anySurfaceOpen is true while any modal, overlay, popover or menu is
// mounted. Page-level shortcuts use it to stay out of the way.
export function anySurfaceOpen(): boolean {
  return queryNested().length > 0;
}
