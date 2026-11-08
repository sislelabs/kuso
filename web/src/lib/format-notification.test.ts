import { describe, expect, it } from "vitest";
import { humanizeNotification, type TextPart } from "./format-notification";

const flat = (parts: TextPart[]) => parts.map((p) => (p.code ? `[${p.text}]` : p.text)).join("");

describe("humanizeNotification", () => {
  it("renders backticks as code and Go durations in words", () => {
    const n = humanizeNotification({
      type: "alert.fired",
      title: "✗ Alert · Worker errors · shop / worker",
      body: "`ERROR` matched 3 times in 5m0s on shop/worker",
    });
    expect(n.title).toBe("Alert · Worker errors · shop / worker");
    expect(flat(n.body)).toBe("[ERROR] matched 3 times in 5 min on shop/worker");
  });

  it("shortens build and image ids to the commit", () => {
    const n = humanizeNotification({
      type: "deploy.rolledback",
      title: "↩ Rolled back · shop / api → production",
      body: "admin rolled production back to build `shop-api-c850e9b0ef35-mw883yzn` (image `c850e9b0ef35-mw883yzn`).\nPreviously live: `shop-api-64cd286f1547-mw88kgv5`.",
    });
    expect(n.title).toBe("Rolled back · shop / api → production");
    expect(flat(n.body)).toBe("admin rolled production back to build [c850e9b].\nPreviously live: [64cd286].");
  });

  it("replaces the Kubernetes Job message with what happened", () => {
    const generic = humanizeNotification({
      type: "build.failed",
      title: "✗ Build failed · shop / api",
      body: "Job has reached the specified backoff limit",
      classification: { kind: "generic", summary: "Deploy failed. See logs for details." },
    });
    expect(flat(generic.body)).toBe("The build failed. Open its log to see why.");
    const classified = humanizeNotification({
      type: "build.failed",
      title: "✗ Build failed",
      body: "Job has reached the specified backoff limit",
      classification: { kind: "clone_dns", summary: "Couldn't reach github.com to clone the repo." },
    });
    expect(flat(classified.body)).toBe("Couldn't reach github.com to clone the repo.");
  });

  it("leaves plain text alone", () => {
    expect(flat(humanizeNotification({ title: "Down", body: "Was down for 6m" }).body)).toBe("Was down for 6m");
  });
});
