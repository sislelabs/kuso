import { describe, expect, it } from "vitest";
import { isUpdateInFlight } from "./updatePhase";

describe("isUpdateInFlight", () => {
  it("treats rollback outcomes as finished", () => {
    expect(isUpdateInFlight("rolled-back")).toBe(false);
    expect(isUpdateInFlight("rollback-failed")).toBe(false);
  });
  it("treats idle, done and failed as finished", () => {
    for (const p of [undefined, "", "done", "failed"]) expect(isUpdateInFlight(p)).toBe(false);
  });
  it("treats other phases as in flight", () => {
    expect(isUpdateInFlight("pulling")).toBe(true);
  });
});
