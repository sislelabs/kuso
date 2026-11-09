import { describe, expect, it } from "vitest";
import { sortBySeverity } from "./listSort";

describe("sortBySeverity", () => {
  it("puts failing first, then by name, whatever the input order", () => {
    const rows = [
      { name: "web", status: { tone: "success" as const } },
      { name: "api", status: { tone: "success" as const } },
      { name: "worker", status: { tone: "error" as const } },
      { name: "cron", status: { tone: "muted" as const } },
      { name: "admin", status: { tone: "warning" as const } },
    ];
    const names = (r: typeof rows) => sortBySeverity(r).map((x) => x.name);
    expect(names(rows)).toEqual(["worker", "admin", "api", "web", "cron"]);
    expect(names([...rows].reverse())).toEqual(names(rows));
  });
});
