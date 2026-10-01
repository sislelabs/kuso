import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { DiffConfirmDialog } from "@/components/shared/DiffConfirmDialog";

describe("DiffConfirmDialog", () => {
  it("labels the dialog by its title", () => {
    const html = renderToStaticMarkup(
      <DiffConfirmDialog open title="Apply env" entries={[]} onCancel={() => {}} onConfirm={() => {}} />,
    );
    const labelledBy = html.match(/aria-labelledby="([^"]+)"/)?.[1];
    expect(labelledBy).toBeTruthy();
    expect(html).toMatch(new RegExp(`<h2 id="${labelledBy}"[^>]*>Apply env</h2>`));
  });
});
