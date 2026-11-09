import { beforeEach, describe, expect, it } from "vitest";
import {
  __resetStagedForTests,
  clearApplied,
  countServiceChanges,
  getStagedState,
  persistable,
  stageEnv,
  stageSettings,
} from "./store";

describe("staged store", () => {
  beforeEach(() => __resetStagedForTests());

  it("drops empty services and projects", () => {
    stageEnv("p", "api", { set: [{ name: "A", value: "1", before: null, sensitive: false }], unset: [] });
    expect(countServiceChanges(getStagedState().p?.api)).toBe(1);
    stageEnv("p", "api", undefined);
    expect(getStagedState().p).toBeUndefined();
  });

  it("clearApplied keeps the items that failed", () => {
    stageEnv("p", "api", {
      set: [
        { name: "A", value: "1", before: null, sensitive: false },
        { name: "B", value: "2", before: null, sensitive: false },
      ],
      unset: [],
    });
    stageSettings("p", "api", { fields: { port: "9090" }, base: { port: "8080" } });
    clearApplied("p", "api", { env: ["A"], settings: true });
    const svc = getStagedState().p?.api;
    expect(svc?.env?.set.map((e) => e.name)).toEqual(["B"]);
    expect(svc?.settings).toBeUndefined();
  });

  it("never persists secret values or the repo token", () => {
    stageEnv("p", "api", {
      set: [
        { name: "API_KEY", value: "sk_live", before: null, sensitive: true },
        { name: "LOG_LEVEL", value: "debug", before: "info", sensitive: false },
      ],
      unset: [{ name: "OLD_TOKEN", before: "t0k", sensitive: true }],
    });
    stageSettings("p", "api", { fields: { repoToken: "glpat-x", port: "9090" }, base: { port: "8080" } });
    const out = JSON.stringify(persistable(getStagedState()));
    expect(out).not.toContain("sk_live");
    expect(out).not.toContain("t0k");
    expect(out).not.toContain("glpat-x");
    expect(out).toContain("LOG_LEVEL");
    expect(out).toContain("9090");
  });
});
