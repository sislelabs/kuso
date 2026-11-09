import { describe, expect, it } from "vitest";
import { nextSteps, welcomeProgress } from "./progress";

describe("welcomeProgress", () => {
  it("starts at step 1 on a fresh install", () => {
    expect(welcomeProgress(false, [])).toMatchObject({ step: 1, done: { 1: false, 2: false, 3: false } });
  });

  it("skips to step 2 once GitHub is connected", () => {
    expect(welcomeProgress(true, []).step).toBe(2);
  });

  it("goes to step 3 for the newest project without services, even without GitHub", () => {
    const p = welcomeProgress(false, [
      { name: "old-empty", createdAt: "2026-01-01T00:00:00Z", serviceCount: 0 },
      { name: "new-empty", createdAt: "2026-05-01T00:00:00Z", serviceCount: 0 },
      { name: "live", createdAt: "2026-06-01T00:00:00Z", serviceCount: 2 },
    ]);
    expect(p).toMatchObject({ step: 3, project: "new-empty", allDeployed: false, done: { 1: false, 2: true, 3: true } });
  });

  it("reports all deployed when every project runs a service", () => {
    expect(welcomeProgress(true, [{ name: "a", serviceCount: 1 }]).allDeployed).toBe(true);
  });

  it("does not mark deploy done when service counts are unknown", () => {
    const p = welcomeProgress(true, [{ name: "a", serviceCount: null }]);
    expect(p).toMatchObject({ step: 3, project: "a", done: { 3: false } });
  });
});

describe("nextSteps", () => {
  const base = { hasGitHub: true, environments: [], userCount: 1, alertRuleCount: 0 };
  it("marks steps done from existing data and lists open ones first", () => {
    const steps = nextSteps({
      ...base,
      environments: [{ envFromSecrets: ["shop-db-conn"], additionalHosts: [] }],
    });
    expect(steps.map((s) => [s.id, s.done])).toEqual([
      ["domain", false],
      ["teammate", false],
      ["alerts", false],
      ["github", true],
      ["database", true],
    ]);
  });
  it("leaves out steps the viewer can't check", () => {
    const ids = nextSteps({ ...base, userCount: null, alertRuleCount: null }).map((s) => s.id);
    expect(ids).not.toContain("teammate");
    expect(ids).not.toContain("alerts");
  });
  it("doesn't count a service's own secret as a database", () => {
    const db = nextSteps({ ...base, environments: [{ envFromSecrets: ["shop-api-secrets"] }] }).find((s) => s.id === "database");
    expect(db?.done).toBe(false);
  });
});
