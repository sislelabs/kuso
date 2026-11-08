import { describe, expect, it } from "vitest";
import { welcomeProgress } from "./progress";

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
