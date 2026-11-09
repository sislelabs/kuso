import { describe, expect, it } from "vitest";
import { attentionItems, headline, memoryFootprint } from "./fleetModel";
import type { ProjectCardModel } from "./projectCardModel";

function model(name: string, services: ProjectCardModel["services"], memBytes = 0): ProjectCardModel {
  return { name, services, metrics: { project: name, cpuMillicores: 1, memBytes, pods: 1, envs: 1 } } as ProjectCardModel;
}

describe("attentionItems", () => {
  it("lists only failing, degraded and in-flight services, worst first", () => {
    const items = attentionItems([
      model("b", [
        { name: "web", status: "deploying" },
        { name: "api", status: "live" },
      ]),
      model("a", [
        { name: "worker", status: "degraded" },
        { name: "cron", status: "sleeping" },
        { name: "db", status: "failing" },
      ]),
    ]);
    expect(items.map((i) => `${i.project}/${i.service}`)).toEqual(["a/db", "a/worker", "b/web"]);
  });
});

describe("headline", () => {
  it("leads with problems when there are any", () => {
    const attention = attentionItems([model("a", [{ name: "db", status: "failing" }])]);
    expect(headline({ projects: 2, services: 5, attention, errors: 1200 })).toBe(
      "2 projects · 5 services · 1 needs attention · 1,200 errors in 24h",
    );
  });
  it("says nothing is failing on a quiet fleet", () => {
    expect(headline({ projects: 1, services: 1, attention: [], errors: 0 })).toBe("1 project · 1 service · nothing failing");
  });
});

describe("memoryFootprint", () => {
  it("folds everything past the top N into other", () => {
    const fp = memoryFootprint([model("a", [], 600), model("b", [], 300), model("c", [], 100)], 2);
    expect(fp.total).toBe(1000);
    expect(fp.slices.map((s) => s.project)).toEqual(["a", "b"]);
    expect(fp.other).toBeCloseTo(0.1);
  });
});
