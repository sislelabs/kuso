import { describe, expect, it } from "vitest";
import { globalKeyBlocked, type KeyTarget, type ModalQuery } from "./globalKeyGuard";

// el fakes Element.closest for an element carrying `attrs` on itself or an ancestor.
const el = (attrs: string[]): KeyTarget => ({
  closest: (selector: string) => (attrs.some((a) => selector.includes(`[${a}]`)) ? {} : null),
});
const page = (openModal: boolean): ModalQuery => ({ querySelector: () => (openModal ? {} : null) });

describe("globalKeyBlocked", () => {
  it("lets ? through from the palette's input while the palette animates out", () => {
    // Esc closes the palette; base-ui keeps it mounted with data-closed /
    // data-ending-style and focus in its input for the exit animation.
    const paletteInput = el(["data-closed", "data-ending-style"]);
    expect(globalKeyBlocked(paletteInput, true, false, page(false))).toBe(false);
  });

  it("still ignores keys typed into a field that isn't closing", () => {
    expect(globalKeyBlocked(el([]), true, false, page(false))).toBe(true);
  });

  it("ignores keys while another modal is open, unless it's the help sheet itself", () => {
    expect(globalKeyBlocked(el([]), false, false, page(true))).toBe(true);
    expect(globalKeyBlocked(el([]), false, true, page(true))).toBe(false);
    expect(globalKeyBlocked(null, false, false, page(false))).toBe(false);
  });
});
