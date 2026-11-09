import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { contrastRatio } from "./contrast";

const css = fs.readFileSync(path.resolve(__dirname, "../app/globals.css"), "utf8");

function tokens(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`);
  const open = css.indexOf("{", start);
  let depth = 0;
  let end = open;
  for (; end < css.length; end++) {
    if (css[end] === "{") depth++;
    else if (css[end] === "}" && --depth === 0) break;
  }
  const out: Record<string, string> = {};
  for (const m of css.slice(open, end).matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim();
  return out;
}

function resolver(vars: Record<string, string>) {
  const get = (name: string, depth = 0): string => {
    const v = vars[name];
    const ref = /^var\(--([a-z0-9-]+)\)$/.exec(v ?? "");
    if (ref && depth < 5) return get(ref[1], depth + 1);
    if (!v?.startsWith("#")) throw new Error(`--${name} is not a hex colour: ${v}`);
    return v;
  };
  return get;
}

const SURFACES = ["bg-primary", "bg-secondary", "bg-tertiary", "bg-elevated", "canvas-bg"];
const TEXT = ["text-primary", "text-secondary", "text-tertiary", "muted-foreground", "accent", "link", "success", "warning", "error", "building", "info", "destructive"];
// Placeholder is intentionally fainter than any real value but still legible.
// btn-primary-bg: the primary button must stand out from the page as a shape, not only by its label.
const NON_TEXT = ["text-placeholder", "border-strong", "focus", "selected-border", "btn-primary-bg"];
// Canvas wires carry meaning (which service reads which addon), so they
// need 3:1 against the canvas ground they're drawn on.
const WIRES = ["edge-addon", "edge-ref", "edge-lit"];
// Meta text (timestamps, counts, card footers, captions) is set at
// 11–12px, where AA's 4.5:1 still reads faint on the dark navy cards, so
// it gets headroom on every surface, popovers and hovered rows included.
const META = ["text-tertiary", "muted-foreground"];
const META_SURFACES = [...SURFACES, "popover", "card"];
const FILLS: [string, string][] = [
  ["btn-primary-fg", "btn-primary-bg"],
  ["btn-primary-fg", "btn-primary-bg-hover"],
  ["accent-fg", "accent"],
  ["btn-secondary-fg", "btn-secondary-bg"],
];

const root = tokens(":root");
for (const [theme, vars] of [["light", root], ["dark", { ...root, ...tokens(".dark") }]] as const) {
  describe(`${theme} theme contrast`, () => {
    const get = resolver(vars);
    it.each(TEXT)("--%s reaches 4.5:1 on every surface", (tok) => {
      for (const s of SURFACES) expect(contrastRatio(get(tok), get(s)), `${tok} on ${s}`).toBeGreaterThanOrEqual(4.5);
    });
    it.each(META)("--%s reaches 5.5:1 on every surface and 5:1 on a hovered row", (tok) => {
      for (const s of META_SURFACES) expect(contrastRatio(get(tok), get(s)), `${tok} on ${s}`).toBeGreaterThanOrEqual(5.5);
      expect(contrastRatio(get(tok), get("border-subtle")), `${tok} on border-subtle`).toBeGreaterThanOrEqual(5);
    });
    it.each(NON_TEXT)("--%s reaches 3:1 on every surface", (tok) => {
      for (const s of SURFACES) expect(contrastRatio(get(tok), get(s)), `${tok} on ${s}`).toBeGreaterThanOrEqual(3);
    });
    it.each(WIRES)("--%s reaches 3:1 on the canvas", (tok) => {
      expect(contrastRatio(get(tok), get("canvas-bg"))).toBeGreaterThanOrEqual(3);
    });
    it.each(FILLS)("--%s on --%s reaches 4.5:1", (fg, bg) => {
      expect(contrastRatio(get(fg), get(bg))).toBeGreaterThanOrEqual(4.5);
    });
  });
}
