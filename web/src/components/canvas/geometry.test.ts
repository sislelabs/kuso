import { describe, expect, it } from "vitest";
import { ADDON_NODE_CHROME, addonNodeWidth, facingSides, nextInDirection, readingOrder, serviceNodeWidth, smallNodeWidth, type Box } from "./geometry";

const box = (id: string, x: number, y: number, w = 200, h = 100): Box => ({ id, x, y, w, h });

// Two columns: addons on the left, services on the right.
//   db    api
//   cache web
const grid = [box("db", 0, 0), box("api", 400, 0), box("cache", 0, 200), box("web", 400, 220)];

describe("nextInDirection", () => {
  it("moves to the neighbour in the pressed direction", () => {
    expect(nextInDirection(grid, "db", "right")).toBe("api");
    expect(nextInDirection(grid, "db", "down")).toBe("cache");
    expect(nextInDirection(grid, "web", "left")).toBe("cache");
    expect(nextInDirection(grid, "web", "up")).toBe("api");
  });

  it("prefers the box straight ahead over a nearer diagonal one", () => {
    const boxes = [box("a", 0, 0), box("diag", 250, 150), box("ahead", 600, 0)];
    expect(nextInDirection(boxes, "a", "right")).toBe("ahead");
  });

  it("stays put at the edge of the graph", () => {
    expect(nextInDirection(grid, "db", "left")).toBeNull();
    expect(nextInDirection(grid, "db", "up")).toBeNull();
    expect(nextInDirection(grid, "nope", "up")).toBeNull();
  });
});

describe("readingOrder", () => {
  it("reads rows top to bottom, left to right, tolerating small offsets", () => {
    expect(readingOrder(grid).map((b) => b.id)).toEqual(["db", "api", "cache", "web"]);
  });
});

describe("facingSides", () => {
  it("connects side by side boxes on their facing vertical sides", () => {
    expect(facingSides(grid[0], grid[1])).toEqual({ source: "right", target: "left" });
    expect(facingSides(grid[1], grid[0])).toEqual({ source: "left", target: "right" });
  });

  it("connects stacked boxes top to bottom", () => {
    expect(facingSides(box("a", 0, 0), box("b", 20, 300))).toEqual({ source: "bottom", target: "top" });
    expect(facingSides(box("b", 20, 300), box("a", 0, 0))).toEqual({ source: "top", target: "bottom" });
  });

  it("keeps a diagonal neighbour on horizontal sides", () => {
    expect(facingSides(box("a", 0, 0), box("b", 400, 300))).toEqual({ source: "right", target: "left" });
  });
});

describe("serviceNodeWidth", () => {
  it("keeps short names at the base width and widens for longer ones up to a cap", () => {
    expect(serviceNodeWidth("web")).toBe(280);
    const mid = serviceNodeWidth("notification-dispatcher-worker");
    expect(mid).toBeGreaterThan(280);
    expect(mid).toBeLessThan(380);
    expect(serviceNodeWidth("x".repeat(80))).toBe(380);
  });
});

describe("smallNodeWidth", () => {
  it("keeps short names at the base width and fits a 20-character name", () => {
    expect(smallNodeWidth("db", ADDON_NODE_CHROME)).toBe(220);
    const w = smallNodeWidth("clickhouse-analytics", ADDON_NODE_CHROME);
    // name text (~7.6px/char) plus the card chrome must fit.
    expect(w).toBeGreaterThanOrEqual(ADDON_NODE_CHROME + 20 * 7.6);
    expect(smallNodeWidth("x".repeat(80), ADDON_NODE_CHROME)).toBe(340);
  });
});

describe("addonNodeWidth", () => {
  it("widens for the CVE chip on the status row", () => {
    const row = "Live · ClickHouse";
    expect(addonNodeWidth("clickhouse", row, true)).toBeGreaterThan(addonNodeWidth("clickhouse", row, false));
  });
});
