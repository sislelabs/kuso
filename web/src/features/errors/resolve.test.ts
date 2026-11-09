import { describe, expect, it } from "vitest";
import { sinceDeployWindow } from "./resolve";

describe("sinceDeployWindow", () => {
  const now = Date.parse("2026-11-09T12:00:00Z");
  it("rounds the time since the deploy up to whole minutes", () => {
    expect(sinceDeployWindow("2026-11-09T10:25:30Z", now)).toBe("95m");
    expect(sinceDeployWindow("2026-11-09T11:59:59Z", now)).toBe("1m");
  });
  it("caps at 30 days and refuses a missing deploy time", () => {
    expect(sinceDeployWindow("2026-01-01T00:00:00Z", now)).toBe(`${30 * 24 * 60}m`);
    expect(sinceDeployWindow(undefined, now)).toBeNull();
    expect(sinceDeployWindow("nope", now)).toBeNull();
  });
});
