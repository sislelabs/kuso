import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { parseInlineMarkdown } from "@/features/marketplace/present";
import { renderMarkdown } from "./InlineMarkdown";

const html = (src: string, links?: boolean) => renderToStaticMarkup(<>{renderMarkdown(parseInlineMarkdown(src), { links })}</>);

describe("renderMarkdown", () => {
  it("escapes raw HTML instead of injecting it", () => {
    const out = html('<img src=x onerror="alert(1)"> `<b>`');
    expect(out).not.toContain("<img");
    expect(out).toContain("&lt;img");
    expect(out).toContain("<code");
    expect(out).toContain("&lt;b&gt;");
  });

  it("opens http(s) links in a new tab with noopener noreferrer", () => {
    const out = html("[docs](https://example.com)");
    expect(out).toMatch(/<a href="https:\/\/example.com\/" target="_blank" rel="noopener noreferrer"/);
    expect(html("[x](javascript:alert(1))")).not.toContain("<a");
  });

  it("renders link text without an anchor when links are off", () => {
    const out = html("**bold** [docs](https://example.com)", false);
    expect(out).toContain("<strong");
    expect(out).not.toContain("<a");
    expect(out).toContain("docs");
  });
});
