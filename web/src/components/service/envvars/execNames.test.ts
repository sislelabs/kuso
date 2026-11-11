import { describe, expect, it } from "vitest";
import { isExecEnvName } from "./execNames";

describe("isExecEnvName", () => {
  const list = { names: ["NODE_OPTIONS", "LD_PRELOAD"], prefixes: ["NIXPACKS_"] };
  it("matches exact names and prefixes, ignoring case", () => {
    expect(isExecEnvName(list, "node_options")).toBe(true);
    expect(isExecEnvName(list, "NIXPACKS_START_CMD")).toBe(true);
    expect(isExecEnvName(list, "DATABASE_URL")).toBe(false);
    expect(isExecEnvName(undefined, "NODE_OPTIONS")).toBe(false);
  });
});
