import { describe, expect, it } from "vitest";
import { actorInitials, actorLabel, isSystemActor, isToggleKey } from "./actor";

describe("isSystemActor", () => {
  it("treats empty, system, kuso and system:* as automated", () => {
    for (const id of [undefined, "", " ", "system", "SYSTEM", "kuso", "system:remediate"]) {
      expect(isSystemActor(id)).toBe(true);
    }
  });

  it("leaves people and user ids alone", () => {
    for (const id of ["ivo9999", "1", "cmb2x9k0a0000abcd", "kusoadmin"]) {
      expect(isSystemActor(id)).toBe(false);
    }
  });
});

describe("actorLabel / actorInitials", () => {
  it("names a blank actor and shortens long ids", () => {
    expect(actorLabel("")).toBe("system");
    expect(actorLabel("kuso")).toBe("kuso");
    expect(actorLabel("cmb2x9k0a0000abcd")).toBe("cmb2x9k0…");
  });

  it("builds initials from letters and digits only", () => {
    expect(actorInitials("ivo9999")).toBe("IV");
    expect(actorInitials("_x")).toBe("X");
    expect(actorInitials("--")).toBe("?");
  });
});

describe("isToggleKey", () => {
  it("accepts Enter and Space only", () => {
    expect(isToggleKey("Enter")).toBe(true);
    expect(isToggleKey(" ")).toBe(true);
    expect(isToggleKey("Tab")).toBe(false);
  });
});
