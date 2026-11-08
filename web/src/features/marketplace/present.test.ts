import { describe, expect, it } from "vitest";
import type { MarketplaceApp } from "./api";
import {
  LETTERMARK_TINTS,
  lettermark,
  lettermarkTint,
  markdownToPlain,
  parseInlineMarkdown,
  setupRisk,
  truncateMarkdown,
  versionDisplay,
} from "./present";

describe("parseInlineMarkdown", () => {
  it("parses code, bold, italics and links", () => {
    expect(parseInlineMarkdown("Run `kuso shell **x**`, then **really** *now* and _here_ via [docs](https://x.dev/a)")).toEqual([
      { kind: "text", text: "Run " },
      { kind: "code", text: "kuso shell **x**" },
      { kind: "text", text: ", then " },
      { kind: "strong", children: [{ kind: "text", text: "really" }] },
      { kind: "text", text: " " },
      { kind: "em", children: [{ kind: "text", text: "now" }] },
      { kind: "text", text: " and " },
      { kind: "em", children: [{ kind: "text", text: "here" }] },
      { kind: "text", text: " via " },
      { kind: "link", href: "https://x.dev/a", children: [{ kind: "text", text: "docs" }] },
    ]);
  });

  it("drops non-http(s) links to their text", () => {
    for (const href of ["javascript:void0", "data:text/html,x", "//evil.example", "/relative", "mailto:a@b.c"]) {
      expect(parseInlineMarkdown(`see [here](${href}) now`)).toEqual([{ kind: "text", text: "see here now" }]);
    }
  });

  it("leaves snake_case, lone stars and unclosed markers literal", () => {
    for (const s of ["GOTIFY_DEFAULTUSER_PASS in env", "2 * 3 * 4", "a `b", "**open", "<script>alert(1)</script>"]) {
      expect(parseInlineMarkdown(s)).toEqual([{ kind: "text", text: s }]);
    }
  });
});

describe("truncateMarkdown", () => {
  const nodes = parseInlineMarkdown("Self-hosted Git service. Create the admin with `kuso shell <project> gitea` then log in.");

  it("returns input untouched when it fits", () => {
    expect(truncateMarkdown(nodes, 500)).toEqual({ nodes, truncated: false });
  });

  it("cuts at a word boundary, never mid-word", () => {
    const { nodes: out, truncated } = truncateMarkdown(nodes, 30);
    expect(truncated).toBe(true);
    expect(markdownToPlain(out)).toBe("Self-hosted Git service");
  });

  it("never splits a code span", () => {
    const out = markdownToPlain(truncateMarkdown(nodes, 60).nodes);
    expect(out).toBe("Self-hosted Git service. Create the admin with");
    expect(markdownToPlain(truncateMarkdown(nodes, 75).nodes)).toBe(
      "Self-hosted Git service. Create the admin with kuso shell <project> gitea",
    );
  });
});

describe("versionDisplay", () => {
  it("shows semver and calver as-is", () => {
    expect(versionDisplay("1.62.1")).toEqual({ label: "v1.62.1", literal: true });
    expect(versionDisplay("2026-11-02")).toEqual({ label: "2026-11-02", literal: true });
    expect(versionDisplay("20241022")).toEqual({ label: "20241022", literal: true });
  });

  it("names commit-pinned and digest-pinned images 'latest build'", () => {
    expect(versionDisplay("4bfc5bb")).toEqual({ label: "latest build", detail: "Built from commit 4bfc5bb", literal: false });
    expect(versionDisplay("sha-4bfc5bb")?.detail).toBe("Built from commit 4bfc5bb");
    expect(versionDisplay(`sha256:${"a".repeat(64)}`)?.label).toBe("latest build");
  });

  it("returns null without a version", () => {
    expect(versionDisplay(undefined)).toBeNull();
    expect(versionDisplay(" ")).toBeNull();
  });
});

describe("lettermark", () => {
  it("is deterministic and spreads across tints", () => {
    expect(lettermarkTint("gotify")).toBe(lettermarkTint("gotify"));
    const names = ["ackee", "cyberchef", "gotify", "homer", "kroki", "linkding", "miniflux", "nocodb", "shlink", "typesense"];
    expect(new Set(names.map(lettermarkTint)).size).toBeGreaterThan(2);
    for (const n of names) expect(LETTERMARK_TINTS).toContain(lettermarkTint(n));
  });

  it("uses the first letter or digit of the title", () => {
    expect(lettermark("it-tools")).toBe("I");
    expect(lettermark("  .ntfy")).toBe("N");
    expect(lettermark("")).toBe("?");
  });
});

describe("setupRisk", () => {
  const app = (over: Partial<MarketplaceApp>): MarketplaceApp => ({
    name: "x",
    title: "X",
    description: "",
    category: "data",
    prompts: [],
    ...over,
  });

  it("uses the server's flags when sent", () => {
    expect(setupRisk(app({ name: "grafana", firstRunSetup: true }))).toEqual({ firstRunSetup: true, defaultCredentials: undefined });
    expect(setupRisk(app({ name: "umami", defaultCredentials: "root/root" }))?.defaultCredentials).toBe("root/root");
  });

  it("falls back for the known apps when an older server omits the flags", () => {
    expect(setupRisk(app({ name: "umami" }))).toEqual({ firstRunSetup: false, defaultCredentials: "admin/umami" });
    for (const name of ["n8n", "metabase", "uptime-kuma", "plausible"]) {
      expect(setupRisk(app({ name }))?.firstRunSetup).toBe(true);
    }
    expect(setupRisk(app({ name: "grafana" }))).toBeNull();
  });
});
