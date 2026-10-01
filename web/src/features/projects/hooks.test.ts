import { describe, expect, it } from "vitest";
import { projectRefetchInterval } from "./hooks";

const env = (state?: string) => ({ status: state ? { state } : undefined });

describe("projectRefetchInterval", () => {
  it("polls fast while any env is mid-transition", () => {
    for (const s of ["building", "deploying", "crashlooping", "degraded"]) {
      expect(projectRefetchInterval({ environments: [env("running"), env(s)] })).toBe(5_000);
    }
  });

  it("still polls, slowly, when everything is steady or unknown", () => {
    expect(projectRefetchInterval({ environments: [env("running"), env("sleeping"), env()] })).toBe(30_000);
    expect(projectRefetchInterval(undefined)).toBe(30_000);
  });
});
