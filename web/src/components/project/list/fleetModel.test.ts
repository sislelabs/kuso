import { describe, expect, it } from "vitest";
import { attentionItems, lastDeployedProject, wallTitle } from "./fleetModel";
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



describe("wallTitle", () => {
  it("names how many services need attention before anything else", () => {
    const attention = attentionItems([
      model("a", [
        { name: "db", status: "failing" },
        { name: "web", status: "deploying" },
      ]),
    ]);
    expect(wallTitle({ attention, running: 3 })).toBe("1 service needs you");
  });
  it("mentions deploys in progress on an otherwise healthy fleet", () => {
    const attention = attentionItems([model("a", [{ name: "web", status: "deploying" }])]);
    expect(wallTitle({ attention, running: 2 })).toBe("Everything is up, 1 deploy in progress");
  });
  it("says so when nothing runs", () => {
    expect(wallTitle({ attention: [], running: 0 })).toBe("Nothing is running");
  });
});

describe("lastDeployedProject", () => {
  it("picks the single most recent production deploy", () => {
    expect(
      lastDeployedProject([
        { name: "a", lastDeployAt: "2026-11-09T10:00:00Z" },
        { name: "b", lastDeployAt: "2026-11-09T12:00:00Z" },
        { name: "c" },
      ]),
    ).toEqual({ name: "b", at: "2026-11-09T12:00:00Z" });
  });
});
