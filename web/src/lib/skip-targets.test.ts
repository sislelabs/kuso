import { describe, expect, it } from "vitest";
import { getSkipTargets, registerSkipTarget } from "./skip-targets";

describe("skip target registry", () => {
  it("adds in order, replaces by id and removes on cleanup", () => {
    const offCanvas = registerSkipTarget({ id: "canvas", label: "Skip to canvas" });
    const offLogs = registerSkipTarget({ id: "logs", label: "Skip to logs" });
    expect(getSkipTargets().map((t) => t.id)).toEqual(["canvas", "logs"]);
    const offRelabel = registerSkipTarget({ id: "canvas", label: "Skip to the canvas" });
    expect(getSkipTargets().map((t) => t.label)).toEqual(["Skip to logs", "Skip to the canvas"]);
    offRelabel();
    offLogs();
    expect(getSkipTargets()).toEqual([]);
    offCanvas();
    expect(getSkipTargets()).toEqual([]);
  });
});
