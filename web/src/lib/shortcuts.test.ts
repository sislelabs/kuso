import { describe, expect, it } from "vitest";
import { getShortcutGroups, isTypingTarget, registerShortcuts } from "./shortcuts";

describe("shortcut registry", () => {
  it("lists registered groups and drops them on cleanup", () => {
    const offA = registerShortcuts("Canvas", [{ keys: ["N"], description: "Add a service" }]);
    const offB = registerShortcuts("Logs", [{ keys: ["/"], description: "Search logs" }]);
    expect(getShortcutGroups().map((g) => g.title)).toEqual(["Canvas", "Logs"]);
    offA();
    expect(getShortcutGroups().map((g) => g.title)).toEqual(["Logs"]);
    offB();
    expect(getShortcutGroups()).toHaveLength(0);
  });

  it("ignores non-element targets", () => {
    expect(isTypingTarget(null)).toBe(false);
  });
});
