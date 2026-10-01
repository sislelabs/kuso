import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { ConfirmDialog, confirmDialogKeyAction } from "@/components/shared/ConfirmDialog";
import { escapeHandledAbove } from "@/lib/escape-layer";

function render(): string {
  return renderToStaticMarkup(
    <ConfirmDialog open title="Delete row" body="gone" onConfirm={() => {}} onCancel={() => {}} />,
  );
}

describe("ConfirmDialog keyboard", () => {
  it("a window-level Enter never confirms; only Escape is handled globally", () => {
    expect(confirmDialogKeyAction("Enter")).toBeNull();
    expect(confirmDialogKeyAction("Escape")).toBe("cancel");
  });

  it("confirm is the form's submit button and the cancel buttons can't submit", () => {
    const html = render();
    expect(html).toContain("<form");
    const buttons = html.match(/<button[^>]*>/g) ?? [];
    const submits = buttons.filter((b) => b.includes('type="submit"'));
    expect(submits).toHaveLength(1);
    expect(buttons.filter((b) => !b.includes('type="submit"')).every((b) => b.includes('type="button"'))).toBe(true);
    const submitIdx = html.indexOf('type="submit"');
    expect(html.slice(submitIdx, html.indexOf("</button>", submitIdx))).toContain("Confirm");
  });
});

describe("escapeHandledAbove", () => {
  const node = (children: object[] = []) => {
    const self = { contains: (o: object | null): boolean => o === self || children.includes(o as object) };
    return self;
  };

  it("an overlay yields Escape to a dialog stacked inside it", () => {
    const overlay = node();
    const dialog = node();
    expect(escapeHandledAbove({ defaultPrevented: false }, overlay, [overlay, dialog])).toBe(true);
  });

  it("an overlay alone handles its own Escape", () => {
    const overlay = node();
    const page = node([overlay]);
    expect(escapeHandledAbove({ defaultPrevented: false }, overlay, [page, overlay])).toBe(false);
  });

  it("a claimed Escape is never handled again", () => {
    expect(escapeHandledAbove({ defaultPrevented: true }, node(), [])).toBe(true);
  });
});
